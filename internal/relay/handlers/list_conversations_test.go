package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const listConvConnID = "conn-list-conv"

// newListConvConn returns a fresh *dispatch.Conn (NextID NOT pre-advanced, so
// the first reply lands at id=1) plus a recv helper that reads one outbound
// envelope. nil auth is fine: the list_conversations handler does not consult
// c.Auth(). Mirrors newCreateConvConn in create_conversation_test.go.
func newListConvConn(t *testing.T) (*dispatch.Conn, func() protocol.RoutingEnvelope) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn(listConvConnID, out, nil)
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

func makeListConversationsRequest(t *testing.T, id uint64) protocol.Envelope {
	t.Helper()
	return protocol.Envelope{
		ID:      id,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage("{}"),
	}
}

func decodeConversationsResponse(t *testing.T, out protocol.RoutingEnvelope) (protocol.Envelope, protocol.ConversationsPayload) {
	t.Helper()
	var inner protocol.Envelope
	if err := json.Unmarshal(out.Frame, &inner); err != nil {
		t.Fatalf("decode inner envelope: %v", err)
	}
	if inner.Type != protocol.TypeConversations {
		t.Fatalf("inner.Type: got %q, want %q", inner.Type, protocol.TypeConversations)
	}
	var payload protocol.ConversationsPayload
	if err := json.Unmarshal(inner.Payload, &payload); err != nil {
		t.Fatalf("decode conversations payload: %v", err)
	}
	return inner, payload
}

func TestListConversations_EmptyRegistry(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	c, recv := newListConvConn(t)

	h := ListConversations(reg)
	if err := h(context.Background(), c, makeListConversationsRequest(t, 11)); err != nil {
		t.Fatalf("handler: %v", err)
	}

	inner, payload := decodeConversationsResponse(t, recv())

	if inner.InReplyTo == nil || *inner.InReplyTo != 11 {
		t.Errorf("InReplyTo: got %v, want pointer to 11", inner.InReplyTo)
	}
	if inner.ID != 1 {
		t.Errorf("inner.ID: got %d, want 1", inner.ID)
	}
	if inner.TS.IsZero() {
		t.Error("inner.TS: got zero time, want non-zero")
	}
	if payload.Conversations == nil {
		t.Fatal("Conversations: got nil slice, want empty non-nil slice")
	}
	if len(payload.Conversations) != 0 {
		t.Errorf("len(Conversations): got %d, want 0", len(payload.Conversations))
	}
	// Empty list serializes as "[]", not "null".
	wantPayloadBytes := []byte(`{"conversations":[]}`)
	if string(inner.Payload) != string(wantPayloadBytes) {
		t.Errorf("payload bytes: got %s, want %s", inner.Payload, wantPayloadBytes)
	}
}

func TestListConversations_SingleConversation(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	name := "scratch"
	ts := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	reg.Create(conversations.Conversation{
		ID:         "conv-1",
		Name:       &name,
		Cwd:        "/work/proj",
		IsPromoted: false,
		LastUsedAt: ts,
	})

	c, recv := newListConvConn(t)

	h := ListConversations(reg)
	if err := h(context.Background(), c, makeListConversationsRequest(t, 99)); err != nil {
		t.Fatalf("handler: %v", err)
	}

	inner, payload := decodeConversationsResponse(t, recv())

	if inner.InReplyTo == nil || *inner.InReplyTo != 99 {
		t.Errorf("InReplyTo: got %v, want pointer to 99", inner.InReplyTo)
	}
	if len(payload.Conversations) != 1 {
		t.Fatalf("len(Conversations): got %d, want 1", len(payload.Conversations))
	}
	got := payload.Conversations[0]
	want := protocol.ConversationSummary{
		ID:            "conv-1",
		Name:          &name,
		IsPromoted:    false,
		Cwd:           "/work/proj",
		LastMessageTS: ts,
		LastUsedAt:    ts,
	}
	if got.ID != want.ID {
		t.Errorf("ID: got %q, want %q", got.ID, want.ID)
	}
	if got.Name == nil || *got.Name != *want.Name {
		t.Errorf("Name: got %v, want pointer to %q", got.Name, *want.Name)
	}
	if got.IsPromoted != want.IsPromoted {
		t.Errorf("IsPromoted: got %v, want %v", got.IsPromoted, want.IsPromoted)
	}
	if got.Cwd != want.Cwd {
		t.Errorf("Cwd: got %q, want %q", got.Cwd, want.Cwd)
	}
	if !got.LastMessageTS.Equal(want.LastMessageTS) {
		t.Errorf("LastMessageTS: got %v, want %v", got.LastMessageTS, want.LastMessageTS)
	}
	if !got.LastUsedAt.Equal(want.LastUsedAt) {
		t.Errorf("LastUsedAt: got %v, want %v", got.LastUsedAt, want.LastUsedAt)
	}
}

// The list_conversations reply is unfiltered — both active and archived rows
// appear, each tagged with its is_archived flag — so a client can partition
// active vs. archived and count each side without a second query.
func TestListConversations_SurfacesArchivedFlag(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	tActive := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	tArchived := time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC)
	reg.Create(conversations.Conversation{ID: "conv-active", Cwd: "/a", IsArchived: false, LastUsedAt: tActive})
	reg.Create(conversations.Conversation{ID: "conv-archived", Cwd: "/b", IsArchived: true, LastUsedAt: tArchived})

	c, recv := newListConvConn(t)

	h := ListConversations(reg)
	if err := h(context.Background(), c, makeListConversationsRequest(t, 7)); err != nil {
		t.Fatalf("handler: %v", err)
	}

	_, payload := decodeConversationsResponse(t, recv())

	if len(payload.Conversations) != 2 {
		t.Fatalf("len(Conversations): got %d, want 2 (both active and archived surface)", len(payload.Conversations))
	}
	byID := map[string]protocol.ConversationSummary{}
	for _, c := range payload.Conversations {
		byID[c.ID] = c
	}
	if got, ok := byID["conv-active"]; !ok || got.IsArchived {
		t.Errorf("conv-active: got %+v, want present with IsArchived=false", got)
	}
	if got, ok := byID["conv-archived"]; !ok || !got.IsArchived {
		t.Errorf("conv-archived: got %+v, want present with IsArchived=true", got)
	}
}

// summariesByID indexes a decoded reply so a test can assert on one row without
// depending on the reply's sort order.
func summariesByID(rows []protocol.ConversationSummary) map[string]protocol.ConversationSummary {
	byID := make(map[string]protocol.ConversationSummary, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	return byID
}

// Each row's workspace_label is resolved from that row's own cwd. Two rows in
// different workspaces carry their own values and neither inherits the other's,
// and two rows sharing one cwd carry the same label — the label belongs to the
// workspace, not to the thread.
func TestListConversations_WorkspaceLabelIsPerWorkspace(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	ts := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	reg.Create(conversations.Conversation{ID: "conv-alpha-1", Cwd: "/work/alpha", LastUsedAt: ts})
	reg.Create(conversations.Conversation{ID: "conv-alpha-2", Cwd: "/work/alpha", LastUsedAt: ts.Add(time.Hour)})
	reg.Create(conversations.Conversation{ID: "conv-beta", Cwd: "/work/beta", LastUsedAt: ts.Add(2 * time.Hour)})
	alpha := "Tax filing"
	reg.SetWorkspaceLabel("/work/alpha", &alpha)

	c, recv := newListConvConn(t)
	h := ListConversations(reg)
	if err := h(context.Background(), c, makeListConversationsRequest(t, 21)); err != nil {
		t.Fatalf("handler: %v", err)
	}

	_, payload := decodeConversationsResponse(t, recv())
	if len(payload.Conversations) != 3 {
		t.Fatalf("len(Conversations): got %d, want 3", len(payload.Conversations))
	}
	byID := summariesByID(payload.Conversations)

	for _, id := range []string{"conv-alpha-1", "conv-alpha-2"} {
		got, ok := byID[id]
		if !ok {
			t.Fatalf("%s missing from reply: %+v", id, payload.Conversations)
		}
		if got.WorkspaceLabel == nil || *got.WorkspaceLabel != alpha {
			t.Errorf("%s WorkspaceLabel: got %v, want pointer to %q", id, got.WorkspaceLabel, alpha)
		}
	}
	got, ok := byID["conv-beta"]
	if !ok {
		t.Fatalf("conv-beta missing from reply: %+v", payload.Conversations)
	}
	if got.WorkspaceLabel != nil {
		t.Errorf("conv-beta WorkspaceLabel: got pointer to %q, want nil (its own workspace is unlabelled)", *got.WorkspaceLabel)
	}
}

// Before any label is ever set, every row carries an explicit null rather than an
// omitted key. Asserted on the raw reply bytes: a decoded ConversationSummary
// cannot tell a nil pointer from a key that omitempty dropped, which is exactly
// the regression a stray omitempty tag on the field would cause — and a client
// that fails closed on a missing key would reject the whole reply.
func TestListConversations_UnlabelledWorkspaceSendsExplicitNull(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	ts := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	reg.Create(conversations.Conversation{ID: "conv-1", Cwd: "/work/one", LastUsedAt: ts})
	reg.Create(conversations.Conversation{ID: "conv-2", Cwd: "/work/two", LastUsedAt: ts.Add(time.Hour)})

	c, recv := newListConvConn(t)
	h := ListConversations(reg)
	if err := h(context.Background(), c, makeListConversationsRequest(t, 22)); err != nil {
		t.Fatalf("handler: %v", err)
	}

	inner, payload := decodeConversationsResponse(t, recv())
	if len(payload.Conversations) != 2 {
		t.Fatalf("len(Conversations): got %d, want 2", len(payload.Conversations))
	}
	const wantKey = `"workspace_label":null`
	if n := strings.Count(string(inner.Payload), wantKey); n != 2 {
		t.Errorf("payload carries %d occurrences of %s, want 2 (one per row):\n%s", n, wantKey, inner.Payload)
	}
}

// An archived row carries the key exactly like an active one: the handler lists
// unfiltered, and archiving a conversation does not un-name its folder.
func TestListConversations_ArchivedRowCarriesWorkspaceLabel(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	ts := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	reg.Create(conversations.Conversation{ID: "conv-archived", Cwd: "/work/shelved", IsArchived: true, LastUsedAt: ts})
	label := "Shelved work"
	reg.SetWorkspaceLabel("/work/shelved", &label)

	c, recv := newListConvConn(t)
	h := ListConversations(reg)
	if err := h(context.Background(), c, makeListConversationsRequest(t, 23)); err != nil {
		t.Fatalf("handler: %v", err)
	}

	_, payload := decodeConversationsResponse(t, recv())
	if len(payload.Conversations) != 1 {
		t.Fatalf("len(Conversations): got %d, want 1", len(payload.Conversations))
	}
	got := payload.Conversations[0]
	if !got.IsArchived {
		t.Fatalf("IsArchived: got false, want true (fixture seeds an archived row)")
	}
	if got.WorkspaceLabel == nil || *got.WorkspaceLabel != label {
		t.Errorf("WorkspaceLabel: got %v, want pointer to %q", got.WorkspaceLabel, label)
	}
}

// The registry keeps a stored empty label ("", true) distinct from an absent one
// ("", false), so the projection must read presence from the accessor's second
// return. Deriving the pointer from label != "" collapses the two and sends null
// for a label that is genuinely stored. No wire path can store an empty label
// today — rename_workspace refuses a blank one — but the registry API can, and
// the collapse would silently diverge from WorkspaceLabel's stated contract.
func TestListConversations_StoredEmptyLabelIsNotNull(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	ts := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	reg.Create(conversations.Conversation{ID: "conv-1", Cwd: "/work/blank", LastUsedAt: ts})
	empty := ""
	reg.SetWorkspaceLabel("/work/blank", &empty)

	c, recv := newListConvConn(t)
	h := ListConversations(reg)
	if err := h(context.Background(), c, makeListConversationsRequest(t, 24)); err != nil {
		t.Fatalf("handler: %v", err)
	}

	inner, payload := decodeConversationsResponse(t, recv())
	if len(payload.Conversations) != 1 {
		t.Fatalf("len(Conversations): got %d, want 1", len(payload.Conversations))
	}
	if got := payload.Conversations[0].WorkspaceLabel; got == nil || *got != "" {
		t.Errorf("WorkspaceLabel: got %v, want pointer to the empty string", got)
	}
	const wantKey = `"workspace_label":""`
	if !strings.Contains(string(inner.Payload), wantKey) {
		t.Errorf("payload does not carry %s:\n%s", wantKey, inner.Payload)
	}
}

func TestListConversations_DeterministicOrdering(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	tEarly := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
	tMid := time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC)
	tLate := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)

	// Seed in an order opposite to expected sort. Two records share
	// LastUsedAt=tMid; tie is broken by ID ascending ("conv-b" < "conv-c").
	reg.Create(conversations.Conversation{ID: "conv-late", Cwd: "/a", LastUsedAt: tLate})
	reg.Create(conversations.Conversation{ID: "conv-c", Cwd: "/b", LastUsedAt: tMid})
	reg.Create(conversations.Conversation{ID: "conv-b", Cwd: "/c", LastUsedAt: tMid})
	reg.Create(conversations.Conversation{ID: "conv-early", Cwd: "/d", LastUsedAt: tEarly})

	c, recv := newListConvConn(t)

	h := ListConversations(reg)
	if err := h(context.Background(), c, makeListConversationsRequest(t, 5)); err != nil {
		t.Fatalf("handler: %v", err)
	}

	_, payload := decodeConversationsResponse(t, recv())

	gotIDs := make([]string, 0, len(payload.Conversations))
	for _, c := range payload.Conversations {
		gotIDs = append(gotIDs, c.ID)
	}
	wantIDs := []string{"conv-early", "conv-b", "conv-c", "conv-late"}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("ids: got %v, want %v", gotIDs, wantIDs)
	}
	for i := range gotIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("ids: got %v, want %v", gotIDs, wantIDs)
		}
	}
}

func TestListConversations_InReplyToAndIDMonotonic(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	// Both replies land on the SAME conn, so NextID advances 1→2 across the
	// two handler invocations (a fresh conn per call would reset it to 1).
	c, recv := newListConvConn(t)
	h := ListConversations(reg)

	if err := h(context.Background(), c, makeListConversationsRequest(t, 100)); err != nil {
		t.Fatalf("first handler: %v", err)
	}
	inner1, _ := decodeConversationsResponse(t, recv())
	if inner1.InReplyTo == nil || *inner1.InReplyTo != 100 {
		t.Errorf("first InReplyTo: got %v, want pointer to 100", inner1.InReplyTo)
	}
	if inner1.ID != 1 {
		t.Errorf("first inner.ID: got %d, want 1", inner1.ID)
	}

	if err := h(context.Background(), c, makeListConversationsRequest(t, 200)); err != nil {
		t.Fatalf("second handler: %v", err)
	}
	inner2, _ := decodeConversationsResponse(t, recv())
	if inner2.InReplyTo == nil || *inner2.InReplyTo != 200 {
		t.Errorf("second InReplyTo: got %v, want pointer to 200", inner2.InReplyTo)
	}
	if inner2.ID != 2 {
		t.Errorf("second inner.ID: got %d, want 2 (monotonic per-conn counter)", inner2.ID)
	}
}

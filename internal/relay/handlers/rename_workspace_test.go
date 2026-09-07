package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	renameWsConnID    = "c-rename-ws"
	renameWsRequestID = uint64(21)
	// renameWsFirstID is the id the handler's first reply must carry: on a fresh
	// conn NextID starts at 1 (the rename_workspace path runs the dispatcher's
	// normal reply machinery, with no gate hello_ack pre-advance).
	renameWsFirstID = uint64(1)
	// renameWsPath is the seeded conversation's cwd — the exact byte string a
	// valid rename must match.
	renameWsPath = "/work/alpha"
	// renameWsOtherPath is a second seeded workspace, used to prove a reject at
	// one path leaves another path's label alone.
	renameWsOtherPath = "/work/beta"
	// renameWsLabel deliberately carries leading and trailing whitespace and a
	// non-ASCII rune: the label is stored VERBATIM (trim is used for the blank
	// check only), so a handler that stored the trimmed value fails here.
	renameWsLabel      = "  Tax filing — Q3  "
	renameWsOtherLabel = "Beta project"
)

// newRenameWsConn returns a fresh *dispatch.Conn (NextID NOT pre-advanced, so the
// first reply lands at id=1) plus a recv helper that reads one outbound envelope.
// nil auth is fine: the rename_workspace handler does not consult c.Auth().
func newRenameWsConn(t *testing.T) (*dispatch.Conn, func() protocol.RoutingEnvelope) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn(renameWsConnID, out, nil)
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

// newRenameWsReg returns a registry backed by a temp-dir path (so the eager Save
// writes to a throwaway file) seeded with two conversations: an active one at
// renameWsPath and an ARCHIVED one at renameWsOtherPath. The archived row is what
// makes the unfiltered-List scan observable — a handler filtering archived rows
// out cannot rename that workspace.
func newRenameWsReg(t *testing.T) (*conversations.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	reg.Create(conversations.Conversation{
		ID:         conversations.ConversationID("conv-alpha"),
		Cwd:        renameWsPath,
		LastUsedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	})
	reg.Create(conversations.Conversation{
		ID:         conversations.ConversationID("conv-beta"),
		Cwd:        renameWsOtherPath,
		IsArchived: true,
		LastUsedAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	})
	return reg, path
}

func renameWsPtr(s string) *string { return &s }

func renameWsRequest(t *testing.T, p protocol.RenameWorkspacePayload) protocol.Envelope {
	t.Helper()
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return protocol.Envelope{
		ID:      renameWsRequestID,
		Type:    protocol.TypeRenameWorkspace,
		TS:      time.Now().UTC(),
		Payload: payloadJSON,
	}
}

func assertRenameWsEnvelopeShape(t *testing.T, resp protocol.RoutingEnvelope, wantType string) protocol.Envelope {
	t.Helper()
	if resp.ConnID != renameWsConnID {
		t.Errorf("Response.ConnID = %q, want %q", resp.ConnID, renameWsConnID)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(resp.Frame, &env); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	if env.Type != wantType {
		t.Errorf("Type = %q, want %q", env.Type, wantType)
	}
	if env.ID != renameWsFirstID {
		t.Errorf("ID = %d, want %d (first reply on a fresh conn)", env.ID, renameWsFirstID)
	}
	if env.InReplyTo == nil || *env.InReplyTo != renameWsRequestID {
		t.Errorf("InReplyTo = %v, want pointer to %d", env.InReplyTo, renameWsRequestID)
	}
	return env
}

// runRenameWs drives the handler once and returns the single outbound envelope.
// The announcer is nil, which is the pre-#2209 shape: every case reached through
// here therefore also covers the nil-hook arm. Cases that need the fan-out use
// runRenameWsAnnouncing below.
func runRenameWs(t *testing.T, reg *conversations.Registry, regPath string, req protocol.Envelope) (protocol.RoutingEnvelope, *dispatch.Conn) {
	t.Helper()
	c, recv := newRenameWsConn(t)
	h := RenameWorkspace(reg, regPath, nil, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	return recv(), c
}

// assertRenameWsReject asserts the four properties every reject branch shares:
// an error envelope, the expected code, non-retryable, and the exact static
// message (never a payload byte).
func assertRenameWsReject(t *testing.T, resp protocol.RoutingEnvelope, wantCode, wantMsg string) {
	t.Helper()
	env := assertRenameWsEnvelopeShape(t, resp, protocol.TypeError)
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
	if payload.Message != wantMsg {
		t.Errorf("Message = %q, want static %q", payload.Message, wantMsg)
	}
}

// TestRenameWorkspace_Success_StoresVerbatimAndReplies covers AC #1: a valid
// rename stores the RAW (untrimmed) label under the exact cwd key and replies
// workspace_updated carrying the path and the stored label, correlated to the
// request.
func TestRenameWorkspace_Success_StoresVerbatimAndReplies(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameWsReg(t)
	resp, _ := runRenameWs(t, reg, regPath, renameWsRequest(t, protocol.RenameWorkspacePayload{
		Path:  renameWsPath,
		Label: renameWsPtr(renameWsLabel),
	}))

	env := assertRenameWsEnvelopeShape(t, resp, protocol.TypeWorkspaceUpdated)
	var payload protocol.WorkspaceUpdatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal workspace_updated payload: %v", err)
	}
	if payload.Path != renameWsPath {
		t.Errorf("reply Path = %q, want %q", payload.Path, renameWsPath)
	}
	if payload.Label == nil || *payload.Label != renameWsLabel {
		t.Errorf("reply Label = %v, want pointer to verbatim %q", payload.Label, renameWsLabel)
	}

	// Stored VERBATIM: the surrounding whitespace survives. A handler that
	// stored strings.TrimSpace(label) fails here.
	got, ok := reg.WorkspaceLabel(renameWsPath)
	if !ok {
		t.Fatalf("WorkspaceLabel(%q): absent, want present", renameWsPath)
	}
	if got != renameWsLabel {
		t.Errorf("stored label = %q, want verbatim %q", got, renameWsLabel)
	}
}

// TestRenameWorkspace_EagerPersist_SurvivesRestart covers AC #1's durability
// clause: the handler Saves eagerly, so a fresh Load FROM DISK returns the label
// (proving it survives a daemon-process restart, which a re-read of the same
// in-memory registry could not).
func TestRenameWorkspace_EagerPersist_SurvivesRestart(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameWsReg(t)
	resp, _ := runRenameWs(t, reg, regPath, renameWsRequest(t, protocol.RenameWorkspacePayload{
		Path:  renameWsPath,
		Label: renameWsPtr(renameWsLabel),
	}))
	assertRenameWsEnvelopeShape(t, resp, protocol.TypeWorkspaceUpdated)

	reloaded, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("reload registry from disk: %v", err)
	}
	got, ok := reloaded.WorkspaceLabel(renameWsPath)
	if !ok {
		t.Fatalf("label absent after reload from disk (the eager Save must run)")
	}
	if got != renameWsLabel {
		t.Errorf("reloaded label = %q, want %q", got, renameWsLabel)
	}
}

// TestRenameWorkspace_ArchivedOnlyWorkspace_IsMatchable covers AC #1's "archived
// rows included": the only conversation at renameWsOtherPath is archived, and the
// rename must still succeed. Pins that the cwd scan ranges the UNFILTERED list.
func TestRenameWorkspace_ArchivedOnlyWorkspace_IsMatchable(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameWsReg(t)
	resp, _ := runRenameWs(t, reg, regPath, renameWsRequest(t, protocol.RenameWorkspacePayload{
		Path:  renameWsOtherPath,
		Label: renameWsPtr(renameWsOtherLabel),
	}))

	assertRenameWsEnvelopeShape(t, resp, protocol.TypeWorkspaceUpdated)
	got, ok := reg.WorkspaceLabel(renameWsOtherPath)
	if !ok || got != renameWsOtherLabel {
		t.Errorf("WorkspaceLabel(%q) = (%q, %v), want (%q, true) — archived rows are matchable",
			renameWsOtherPath, got, ok, renameWsOtherLabel)
	}
}

// TestRenameWorkspace_NullLabel_ClearsToAbsent covers AC #2: a null label clears
// the stored label so a later read reports it ABSENT (not a present empty
// string), and the reply carries an explicit null. The reply assertion reads the
// RAW JSON rather than the decoded pointer, because an accidental `omitempty`
// would decode to nil either way — only the bytes tell the two apart.
func TestRenameWorkspace_NullLabel_ClearsToAbsent(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameWsReg(t)

	// Set first, so the clear has something to remove.
	resp, _ := runRenameWs(t, reg, regPath, renameWsRequest(t, protocol.RenameWorkspacePayload{
		Path:  renameWsPath,
		Label: renameWsPtr(renameWsLabel),
	}))
	assertRenameWsEnvelopeShape(t, resp, protocol.TypeWorkspaceUpdated)

	resp, _ = runRenameWs(t, reg, regPath, renameWsRequest(t, protocol.RenameWorkspacePayload{
		Path:  renameWsPath,
		Label: nil,
	}))
	env := assertRenameWsEnvelopeShape(t, resp, protocol.TypeWorkspaceUpdated)
	if !bytes.Contains(env.Payload, []byte(`"label":null`)) {
		t.Errorf("reply payload = %s, want an explicit \"label\":null", env.Payload)
	}

	if got, ok := reg.WorkspaceLabel(renameWsPath); ok {
		t.Errorf("WorkspaceLabel(%q) = (%q, true), want absent — a clear must not leave a present empty string",
			renameWsPath, got)
	}
}

// TestRenameWorkspace_NotFound_StoresNothing covers AC #3's not-found branch: a
// path matching no conversation's cwd is refused with workspace.not_found and a
// fixed static message, and a label already stored at ANOTHER path is untouched.
func TestRenameWorkspace_NotFound_StoresNothing(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameWsReg(t)
	reg.SetWorkspaceLabel(renameWsOtherPath, renameWsPtr(renameWsOtherLabel))

	unknown := "/work/nowhere"
	resp, _ := runRenameWs(t, reg, regPath, renameWsRequest(t, protocol.RenameWorkspacePayload{
		Path:  unknown,
		Label: renameWsPtr("Anything"),
	}))
	assertRenameWsReject(t, resp, protocol.CodeWorkspaceNotFound, msgRenameWorkspaceNotFound)

	if got, ok := reg.WorkspaceLabel(unknown); ok {
		t.Errorf("WorkspaceLabel(%q) = (%q, true), want absent — a reject stores nothing", unknown, got)
	}
	if got, ok := reg.WorkspaceLabel(renameWsOtherPath); !ok || got != renameWsOtherLabel {
		t.Errorf("WorkspaceLabel(%q) = (%q, %v), want unchanged (%q, true)",
			renameWsOtherPath, got, ok, renameWsOtherLabel)
	}
}

// TestRenameWorkspace_ClearOnUnknownPath_IsRefused pins that the not-found
// refusal is unqualified: it applies to a CLEAR as much as to a set, rather than
// silently succeeding because a clear has no value to store.
func TestRenameWorkspace_ClearOnUnknownPath_IsRefused(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameWsReg(t)
	resp, _ := runRenameWs(t, reg, regPath, renameWsRequest(t, protocol.RenameWorkspacePayload{
		Path:  "/work/nowhere",
		Label: nil,
	}))
	assertRenameWsReject(t, resp, protocol.CodeWorkspaceNotFound, msgRenameWorkspaceNotFound)
}

// TestRenameWorkspace_BlankLabel_StoresNothing covers AC #3's blank branch: a
// label that is empty or whitespace-only after trimming is refused with
// protocol.malformed, and a label already stored at that path is unchanged (the
// guard runs BEFORE the store).
func TestRenameWorkspace_BlankLabel_StoresNothing(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, label string }{
		{"empty", ""},
		{"spaces", "   "},
		{"tab and newline", "\t\n "},
		{"single space", " "},
	} {
		label := tt.label
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg, regPath := newRenameWsReg(t)
			reg.SetWorkspaceLabel(renameWsPath, renameWsPtr(renameWsLabel))

			resp, _ := runRenameWs(t, reg, regPath, renameWsRequest(t, protocol.RenameWorkspacePayload{
				Path:  renameWsPath,
				Label: renameWsPtr(label),
			}))
			assertRenameWsReject(t, resp, protocol.CodeProtocolMalformed, msgRenameWorkspaceBlankLabel)

			if got, ok := reg.WorkspaceLabel(renameWsPath); !ok || got != renameWsLabel {
				t.Errorf("WorkspaceLabel(%q) = (%q, %v), want unchanged (%q, true)",
					renameWsPath, got, ok, renameWsLabel)
			}
		})
	}
}

// TestRenameWorkspace_LabelByteBound covers AC #3's length branch: an over-bound
// label is REFUSED, never truncated, while a label at exactly the bound is
// accepted — so an off-by-one in either direction reddens. The multi-byte arm
// pins BYTES, NOT RUNES: its rune count is far under the bound while its byte
// count is over it.
func TestRenameWorkspace_LabelByteBound(t *testing.T) {
	t.Parallel()
	// "é" is 2 UTF-8 bytes, so 65 of them are 130 bytes over a 128-byte bound
	// while being only 65 runes.
	multiByte := strings.Repeat("é", protocol.MaxWorkspaceLabelBytes/2+1)
	if len([]rune(multiByte)) > protocol.MaxWorkspaceLabelBytes {
		t.Fatalf("multi-byte arm is vacuous: %d runes is not under the %d bound",
			len([]rune(multiByte)), protocol.MaxWorkspaceLabelBytes)
	}

	tests := []struct {
		name     string
		label    string
		accepted bool
	}{
		{"at the bound", strings.Repeat("a", protocol.MaxWorkspaceLabelBytes), true},
		{"one byte over", strings.Repeat("a", protocol.MaxWorkspaceLabelBytes+1), false},
		{"multi-byte over by bytes, under by runes", multiByte, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg, regPath := newRenameWsReg(t)
			resp, _ := runRenameWs(t, reg, regPath, renameWsRequest(t, protocol.RenameWorkspacePayload{
				Path:  renameWsPath,
				Label: renameWsPtr(tt.label),
			}))

			if tt.accepted {
				assertRenameWsEnvelopeShape(t, resp, protocol.TypeWorkspaceUpdated)
				got, ok := reg.WorkspaceLabel(renameWsPath)
				if !ok || got != tt.label {
					t.Errorf("WorkspaceLabel(%q): got (%d bytes, %v), want the %d-byte label stored",
						renameWsPath, len(got), ok, len(tt.label))
				}
				return
			}

			assertRenameWsReject(t, resp, protocol.CodeProtocolMalformed, msgRenameWorkspaceLabelTooLong)
			if got, ok := reg.WorkspaceLabel(renameWsPath); ok {
				t.Errorf("WorkspaceLabel(%q) = (%q, true), want absent — an over-bound label is refused, NEVER truncated",
					renameWsPath, got)
			}
		})
	}
}

// TestRenameWorkspace_Malformed_StoresNothing covers AC #3's decode branch: a
// non-decodable payload yields protocol.malformed carrying the static message
// (the decode-error text must not be echoed) and stores nothing.
func TestRenameWorkspace_Malformed_StoresNothing(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameWsReg(t)
	resp, _ := runRenameWs(t, reg, regPath, protocol.Envelope{
		ID:      renameWsRequestID,
		Type:    protocol.TypeRenameWorkspace,
		TS:      time.Now().UTC(),
		Payload: []byte(`{"path":`),
	})
	assertRenameWsReject(t, resp, protocol.CodeProtocolMalformed, msgRenameWorkspaceMalformed)

	if got, ok := reg.WorkspaceLabel(renameWsPath); ok {
		t.Errorf("WorkspaceLabel(%q) = (%q, true), want absent", renameWsPath, got)
	}
}

// TestRenameWorkspace_NoEcho_RejectRepliesCarryNoPayloadByte covers AC #3's
// no-echo clause across EVERY reject branch: an injected-looking path and label
// appear nowhere in the error envelope's bytes — not in the message, and not
// anywhere else the payload could have leaked into.
func TestRenameWorkspace_NoEcho_RejectRepliesCarryNoPayloadByte(t *testing.T) {
	t.Parallel()
	const injected = "../../etc/passwd<script>"

	tests := []struct {
		name string
		req  func(t *testing.T) protocol.Envelope
	}{
		{"not found", func(t *testing.T) protocol.Envelope {
			return renameWsRequest(t, protocol.RenameWorkspacePayload{
				Path: "/work/" + injected, Label: renameWsPtr(injected),
			})
		}},
		{"blank label", func(t *testing.T) protocol.Envelope {
			return renameWsRequest(t, protocol.RenameWorkspacePayload{
				Path: "/work/" + injected, Label: renameWsPtr("   "),
			})
		}},
		{"over bound", func(t *testing.T) protocol.Envelope {
			return renameWsRequest(t, protocol.RenameWorkspacePayload{
				Path:  "/work/" + injected,
				Label: renameWsPtr(injected + strings.Repeat("z", protocol.MaxWorkspaceLabelBytes)),
			})
		}},
		{"malformed", func(t *testing.T) protocol.Envelope {
			return protocol.Envelope{
				ID:      renameWsRequestID,
				Type:    protocol.TypeRenameWorkspace,
				TS:      time.Now().UTC(),
				Payload: []byte(`{"path":"` + injected + `"`),
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg, regPath := newRenameWsReg(t)
			resp, _ := runRenameWs(t, reg, regPath, tt.req(t))

			env := assertRenameWsEnvelopeShape(t, resp, protocol.TypeError)
			if bytes.Contains(env.Payload, []byte("passwd")) {
				t.Errorf("error payload echoes request bytes: %s", env.Payload)
			}
			if bytes.Contains(resp.Frame, []byte("passwd")) {
				t.Errorf("error frame echoes request bytes: %s", resp.Frame)
			}
		})
	}
}

// TestRenameWorkspace_SaveFailure_StillRepliesAndKeepsInMemory pins the
// best-effort persist contract: a Save that cannot write still replies
// workspace_updated, because the in-memory write already happened and is what
// every subsequent read sees. The blocker-file technique is
// TestRegisterPushToken_SaveFailure's.
func TestRenameWorkspace_SaveFailure_StillRepliesAndKeepsInMemory(t *testing.T) {
	t.Parallel()
	reg, _ := newRenameWsReg(t)

	// A regular file where a directory must be makes MkdirAll fail inside Save.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatalf("create blocker file: %v", err)
	}
	badPath := filepath.Join(blocker, "conversations.json")

	resp, _ := runRenameWs(t, reg, badPath, renameWsRequest(t, protocol.RenameWorkspacePayload{
		Path:  renameWsPath,
		Label: renameWsPtr(renameWsLabel),
	}))

	assertRenameWsEnvelopeShape(t, resp, protocol.TypeWorkspaceUpdated)
	if got, ok := reg.WorkspaceLabel(renameWsPath); !ok || got != renameWsLabel {
		t.Errorf("WorkspaceLabel(%q) = (%q, %v), want (%q, true) — a Save failure must not undo the in-memory write",
			renameWsPath, got, ok, renameWsLabel)
	}
}

// --- #2209: the fan-out to the other connected clients ---------------------

// wsAnnounceCall records one WorkspaceAnnouncer invocation. repliesOut is how
// many envelopes the conn's outbound channel already held when the hook fired,
// which is what pins "fan out AFTER the correlated reply" deterministically
// rather than by reading the code.
type wsAnnounceCall struct {
	payload    protocol.WorkspaceUpdatedPayload
	exclude    string
	repliesOut int
}

// runRenameWsAnnouncing drives the handler once with a recording announcer and
// returns every call it made alongside the single outbound envelope. The sibling
// runRenameWs passes a nil announcer, so every test written before this slice
// keeps covering the nil-hook arm.
func runRenameWsAnnouncing(t *testing.T, reg *conversations.Registry, regPath string, req protocol.Envelope) ([]wsAnnounceCall, protocol.RoutingEnvelope) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn(renameWsConnID, out, nil)
	var calls []wsAnnounceCall
	announce := func(p protocol.WorkspaceUpdatedPayload, excludeConnID string) {
		calls = append(calls, wsAnnounceCall{payload: p, exclude: excludeConnID, repliesOut: len(out)})
	}
	h := RenameWorkspace(reg, regPath, announce, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	select {
	case env := <-out:
		return calls, env
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for outbound envelope")
		return nil, protocol.RoutingEnvelope{}
	}
}

// TestRenameWorkspace_Success_AnnouncesToTheOtherClients covers AC #1's push
// half: a successful rename announces exactly once, carrying the same record the
// reply carries and naming the REQUESTER'S conn as the one to skip. The emitter
// does the skipping; the handler's whole job is to hand over the right key, and
// the key is the conn id because that is the space relay.ActiveConn is in.
func TestRenameWorkspace_Success_AnnouncesToTheOtherClients(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameWsReg(t)
	calls, resp := runRenameWsAnnouncing(t, reg, regPath, renameWsRequest(t, protocol.RenameWorkspacePayload{
		Path:  renameWsPath,
		Label: renameWsPtr(renameWsLabel),
	}))

	env := assertRenameWsEnvelopeShape(t, resp, protocol.TypeWorkspaceUpdated)
	if len(calls) != 1 {
		t.Fatalf("announced %d times, want exactly 1", len(calls))
	}
	if calls[0].exclude != renameWsConnID {
		t.Errorf("exclusion key = %q, want the requester's own conn id %q — it already holds the "+
			"correlated reply", calls[0].exclude, renameWsConnID)
	}
	if calls[0].payload.Path != renameWsPath {
		t.Errorf("announced Path = %q, want %q", calls[0].payload.Path, renameWsPath)
	}
	if calls[0].payload.Label == nil || *calls[0].payload.Label != renameWsLabel {
		t.Errorf("announced Label = %v, want pointer to verbatim %q", calls[0].payload.Label, renameWsLabel)
	}

	// The announced record and the replied one are the same record: a client that
	// learns of the rename by push must see what the requester saw.
	var replied protocol.WorkspaceUpdatedPayload
	if err := json.Unmarshal(env.Payload, &replied); err != nil {
		t.Fatalf("unmarshal workspace_updated payload: %v", err)
	}
	if replied.Path != calls[0].payload.Path {
		t.Errorf("replied Path %q != announced Path %q", replied.Path, calls[0].payload.Path)
	}
	if replied.Label == nil || *replied.Label != *calls[0].payload.Label {
		t.Errorf("replied Label %v != announced Label %v", replied.Label, calls[0].payload.Label)
	}

	// AC #4's ordering clause: the reply was already on its way out when the hook
	// fired, so nothing the fan-out does can precede or displace it.
	if calls[0].repliesOut != 1 {
		t.Errorf("the conn held %d outbound envelopes when the announcement fired, want 1 — the "+
			"fan-out runs after the correlated reply", calls[0].repliesOut)
	}
}

// TestRenameWorkspace_ClearedLabel_AnnouncesANullLabel covers AC #3: clearing
// fans out the same way a set does, carrying the nil the wire renders as null.
// The clear travels through the SAME hook rather than a quieter path — other
// clients need to unname the workspace as much as they need to name it.
func TestRenameWorkspace_ClearedLabel_AnnouncesANullLabel(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameWsReg(t)
	reg.SetWorkspaceLabel(renameWsPath, renameWsPtr(renameWsLabel))
	calls, resp := runRenameWsAnnouncing(t, reg, regPath, renameWsRequest(t, protocol.RenameWorkspacePayload{
		Path:  renameWsPath,
		Label: nil,
	}))

	assertRenameWsEnvelopeShape(t, resp, protocol.TypeWorkspaceUpdated)
	if len(calls) != 1 {
		t.Fatalf("announced %d times, want exactly 1 — a clear fans out like a set", len(calls))
	}
	if calls[0].payload.Label != nil {
		t.Errorf("announced Label = %q, want nil", *calls[0].payload.Label)
	}
	if calls[0].payload.Path != renameWsPath {
		t.Errorf("announced Path = %q, want %q", calls[0].payload.Path, renameWsPath)
	}
}

// TestRenameWorkspace_Rejects_AnnounceNothing pins the structural property every
// reject branch shares with the store: each returns before the hook, so a refused
// rename tells no other client anything. A fan-out on a reject would publish a
// label this daemon never stored.
func TestRenameWorkspace_Rejects_AnnounceNothing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		req  func(t *testing.T) protocol.Envelope
	}{
		{"malformed", func(t *testing.T) protocol.Envelope {
			return protocol.Envelope{
				ID:      renameWsRequestID,
				Type:    protocol.TypeRenameWorkspace,
				TS:      time.Now().UTC(),
				Payload: []byte(`{"path":`),
			}
		}},
		{"blank label", func(t *testing.T) protocol.Envelope {
			return renameWsRequest(t, protocol.RenameWorkspacePayload{
				Path: renameWsPath, Label: renameWsPtr("   "),
			})
		}},
		{"label too long", func(t *testing.T) protocol.Envelope {
			return renameWsRequest(t, protocol.RenameWorkspacePayload{
				Path:  renameWsPath,
				Label: renameWsPtr(strings.Repeat("x", protocol.MaxWorkspaceLabelBytes+1)),
			})
		}},
		{"not found", func(t *testing.T) protocol.Envelope {
			return renameWsRequest(t, protocol.RenameWorkspacePayload{
				Path: "/work/nonexistent", Label: renameWsPtr(renameWsLabel),
			})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, regPath := newRenameWsReg(t)
			calls, resp := runRenameWsAnnouncing(t, reg, regPath, tc.req(t))

			assertRenameWsEnvelopeShape(t, resp, protocol.TypeError)
			if len(calls) != 0 {
				t.Errorf("a refused rename announced %d times, want 0 — nothing was stored, so there "+
					"is nothing to tell another client", len(calls))
			}
		})
	}
}

// TestRenameWorkspace_NilAnnounce_LeavesTheOperationUnchanged covers AC #4's
// nil-hook clause: a handler built without a fan-out stores, persists and replies
// exactly as it did before this slice. The whole pre-#2209 suite runs this way
// through runRenameWs; this names the property so it cannot be lost by accident.
func TestRenameWorkspace_NilAnnounce_LeavesTheOperationUnchanged(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameWsReg(t)
	c, recv := newRenameWsConn(t)
	h := RenameWorkspace(reg, regPath, nil, testLogger(t))
	if err := h(context.Background(), c, renameWsRequest(t, protocol.RenameWorkspacePayload{
		Path:  renameWsPath,
		Label: renameWsPtr(renameWsLabel),
	})); err != nil {
		t.Fatalf("handler with a nil announcer returned %v, want nil", err)
	}

	assertRenameWsEnvelopeShape(t, recv(), protocol.TypeWorkspaceUpdated)
	if got, ok := reg.WorkspaceLabel(renameWsPath); !ok || got != renameWsLabel {
		t.Errorf("stored label = (%q, %v), want (%q, true)", got, ok, renameWsLabel)
	}
	// Persisted, not just held: a nil hook must not have skipped the eager Save.
	reloaded, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	if got, ok := reloaded.WorkspaceLabel(renameWsPath); !ok || got != renameWsLabel {
		t.Errorf("label on disk = (%q, %v), want (%q, true)", got, ok, renameWsLabel)
	}
}

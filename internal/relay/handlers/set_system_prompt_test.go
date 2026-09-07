package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	sspConnID    = "c-set-sysprompt"
	sspRequestID = uint64(41)
	// sspFirstID is the id the handler's first reply must carry: on a fresh conn
	// NextID starts at 1 (set_system_prompt runs the dispatcher's normal reply
	// machinery, with no gate hello_ack pre-advance).
	sspFirstID = uint64(1)
	// sspTargetID is the seeded conversation's stable id — the exact registry key
	// a valid set_system_prompt must match. Deliberately a long, unique string
	// that collides with no other value in this file: the leak assertions search
	// the log buffer for it as a substring, and a short or common id would make
	// "absent" prove nothing.
	sspTargetID = "conv-ssp-target-9d41f-never-logged"
	// sspBystanderID is a second seeded row that no request ever names. It is what
	// makes "exactly the conversation the frame names changes" observable rather
	// than vacuously true against a single-row registry.
	sspBystanderID = "conv-ssp-bystander-2a7c"
	sspTargetName  = "target-channel"
	sspCwd         = "/work/ssp"
	// sspPrompt is the value a successful set stores. Unique for the same reason
	// as sspTargetID — every leak assertion is a substring search for it.
	sspPrompt = "SSP-PROMPT-SENTINEL-9f3a: answer only in haiku."
	// sspSeeded is the prompt the target row already carries before each test. A
	// reject must leave THIS value in place; without a pre-seeded value, "nothing
	// was persisted" would hold trivially against an empty starting state.
	sspSeeded = "SSP-SEEDED-SENTINEL-b21e: the value a reject must preserve."
)

// sspSeedTime is the seeded rows' LastUsedAt — a fixed instant so tests can
// assert it is NOT bumped by what is a metadata edit.
var sspSeedTime = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func strptr(s string) *string { return &s }

// newSSPConn returns a fresh *dispatch.Conn (NextID NOT pre-advanced, so the
// first reply lands at id=1) plus a recv helper that reads one outbound
// envelope. nil auth is fine: the set_system_prompt handler does not consult
// c.Auth().
func newSSPConn(t *testing.T) (*dispatch.Conn, func() protocol.RoutingEnvelope) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn(sspConnID, out, nil)
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

// newSSPReg returns a registry backed by a temp-dir path, seeded with the target
// row (already carrying sspSeeded) and one bystander row, then Saved once so the
// on-disk file exists and reject tests can prove it is byte-identical afterwards.
func newSSPReg(t *testing.T) (*conversations.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	name := sspTargetName
	reg.Create(conversations.Conversation{
		ID:           conversations.ConversationID(sspTargetID),
		Name:         &name,
		Cwd:          sspCwd,
		IsPromoted:   true,
		LastUsedAt:   sspSeedTime,
		SystemPrompt: strptr(sspSeeded),
	})
	reg.Create(conversations.Conversation{
		ID:         conversations.ConversationID(sspBystanderID),
		Cwd:        sspCwd,
		LastUsedAt: sspSeedTime,
	})
	if err := reg.Save(path); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	return reg, path
}

func sspRequest(t *testing.T, p protocol.SetSystemPromptPayload) protocol.Envelope {
	t.Helper()
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return sspRawRequest(payloadJSON)
}

func sspRawRequest(payload []byte) protocol.Envelope {
	return protocol.Envelope{
		ID:      sspRequestID,
		Type:    protocol.TypeSetSystemPrompt,
		TS:      time.Now().UTC(),
		Payload: payload,
	}
}

func assertSSPEnvelopeShape(t *testing.T, resp protocol.RoutingEnvelope, wantType string) protocol.Envelope {
	t.Helper()
	if resp.ConnID != sspConnID {
		t.Errorf("Response.ConnID = %q, want %q", resp.ConnID, sspConnID)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(resp.Frame, &env); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	if env.Type != wantType {
		t.Errorf("Type = %q, want %q", env.Type, wantType)
	}
	if env.ID != sspFirstID {
		t.Errorf("ID = %d, want %d (first reply on a fresh conn)", env.ID, sspFirstID)
	}
	if env.InReplyTo == nil || *env.InReplyTo != sspRequestID {
		t.Errorf("InReplyTo = %v, want pointer to %d", env.InReplyTo, sspRequestID)
	}
	return env
}

// promptState renders a *string prompt in the only representation that can tell
// the three stored states apart — the same discrimination Pool.conversationPrompt
// makes at the spawn funnel. Comparing pointees alone would collapse "absent"
// and "explicitly empty" into one another.
func promptState(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return "text:" + *p
}

// snapshotRows returns every row keyed by id, for the whole-registry before/after
// comparison that pins "exactly one field of exactly the conversation the frame
// names changes".
func snapshotRows(t *testing.T, reg *conversations.Registry) map[string]conversations.Conversation {
	t.Helper()
	rows := map[string]conversations.Conversation{}
	for _, cv := range reg.List() {
		rows[string(cv.ID)] = cv
	}
	return rows
}

// assertOnlyPromptChanged fails unless every row is field-for-field identical to
// its "before" self except for changedID's SystemPrompt. changedID == "" asserts
// that nothing at all changed. It compares the FULL record (not a hand-picked
// field list) so a future field added to Conversation is covered without this
// test being revisited.
func assertOnlyPromptChanged(t *testing.T, before, after map[string]conversations.Conversation, changedID string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("row count = %d, want %d (no row may be added or removed)", len(after), len(before))
	}
	for id, was := range before {
		now, ok := after[id]
		if !ok {
			t.Fatalf("row %q vanished", id)
		}
		if id != changedID && promptState(now.SystemPrompt) != promptState(was.SystemPrompt) {
			t.Errorf("row %q SystemPrompt = %s, want unchanged %s",
				id, promptState(now.SystemPrompt), promptState(was.SystemPrompt))
		}
		// Blank the one field this verb is allowed to touch, then require the rest
		// of the record to be byte-identical.
		was.SystemPrompt, now.SystemPrompt = nil, nil
		if !reflect.DeepEqual(was, now) {
			t.Errorf("row %q changed a field other than SystemPrompt:\n before %+v\n after  %+v", id, was, now)
		}
	}
}

// TestSetSystemPrompt_Set_PersistsRepliesAndTouchesNothingElse covers AC #1 and
// AC #3's reply half: a valid set stores the value, replies conversation_updated
// WITHOUT carrying the prompt, persists eagerly, and changes exactly one field of
// exactly the named conversation.
func TestSetSystemPrompt_Set_PersistsRepliesAndTouchesNothingElse(t *testing.T) {
	t.Parallel()
	reg, regPath := newSSPReg(t)
	before := snapshotRows(t, reg)
	c, recv := newSSPConn(t)

	h := SetSystemPrompt(reg, regPath, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	req := sspRequest(t, protocol.SetSystemPromptPayload{
		ConversationID: sspTargetID,
		SystemPrompt:   strptr(sspPrompt),
	})
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertSSPEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)

	// The reply confirms the write without carrying the value: no system_prompt
	// key, and not one byte of the prompt anywhere in the frame.
	if strings.Contains(string(env.Payload), sspPrompt) {
		t.Errorf("reply payload echoed the prompt: %s", env.Payload)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &raw); err != nil {
		t.Fatalf("unmarshal reply payload: %v", err)
	}
	if _, ok := raw["system_prompt"]; ok {
		t.Errorf("reply payload carries a system_prompt key: %s", env.Payload)
	}
	var updated protocol.ConversationUpdatedPayload
	if err := json.Unmarshal(env.Payload, &updated); err != nil {
		t.Fatalf("unmarshal conversation_updated: %v", err)
	}
	if updated.ID != sspTargetID {
		t.Errorf("reply id = %q, want %q", updated.ID, sspTargetID)
	}
	if !updated.LastUsedAt.Equal(sspSeedTime) {
		t.Errorf("reply last_used_at = %v, want unbumped %v", updated.LastUsedAt, sspSeedTime)
	}

	// In memory: the target holds the new value, nothing else moved.
	assertOnlyPromptChanged(t, before, snapshotRows(t, reg), sspTargetID)
	cv, ok := reg.Get(conversations.ConversationID(sspTargetID))
	if !ok {
		t.Fatalf("target row vanished")
	}
	if promptState(cv.SystemPrompt) != "text:"+sspPrompt {
		t.Errorf("stored prompt = %s, want %s", promptState(cv.SystemPrompt), "text:"+sspPrompt)
	}

	// On disk: a fresh Load sees it, so the change survives a daemon restart.
	reloaded, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	rl, ok := reloaded.Get(conversations.ConversationID(sspTargetID))
	if !ok {
		t.Fatalf("target row absent after reload")
	}
	if promptState(rl.SystemPrompt) != "text:"+sspPrompt {
		t.Errorf("reloaded prompt = %s, want %s (eager Save did not persist)",
			promptState(rl.SystemPrompt), "text:"+sspPrompt)
	}
}

// TestSetSystemPrompt_TriState covers AC #1's tri-state half: the request can
// express "clear" distinctly from "set it to the empty string", and clearing
// returns the conversation to spawning exactly as it does today (a nil prompt —
// the state Pool.conversationPrompt flattens to "").
func TestSetSystemPrompt_TriState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload []byte
		want    string
	}{
		{"explicit null clears", []byte(`{"conversation_id":"` + sspTargetID + `","system_prompt":null}`), "<nil>"},
		{"absent key clears", []byte(`{"conversation_id":"` + sspTargetID + `"}`), "<nil>"},
		{"empty string is explicitly empty", []byte(`{"conversation_id":"` + sspTargetID + `","system_prompt":""}`), "text:"},
		{"text is stored verbatim", []byte(`{"conversation_id":"` + sspTargetID + `","system_prompt":"` + sspPrompt + `"}`), "text:" + sspPrompt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, regPath := newSSPReg(t)
			before := snapshotRows(t, reg)
			c, recv := newSSPConn(t)

			h := SetSystemPrompt(reg, regPath, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
			if err := h(context.Background(), c, sspRawRequest(tc.payload)); err != nil {
				t.Fatalf("handler: %v", err)
			}
			assertSSPEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)

			cv, ok := reg.Get(conversations.ConversationID(sspTargetID))
			if !ok {
				t.Fatalf("target row vanished")
			}
			if got := promptState(cv.SystemPrompt); got != tc.want {
				t.Errorf("stored prompt = %s, want %s", got, tc.want)
			}
			assertOnlyPromptChanged(t, before, snapshotRows(t, reg), sspTargetID)

			// The tri-state must survive the eager Save → Load round trip, which is
			// where a naive `omitempty` or a value-typed field would collapse
			// "explicitly empty" into "absent".
			reloaded, err := conversations.Load(regPath)
			if err != nil {
				t.Fatalf("reload registry: %v", err)
			}
			rl, ok := reloaded.Get(conversations.ConversationID(sspTargetID))
			if !ok {
				t.Fatalf("target row absent after reload")
			}
			if got := promptState(rl.SystemPrompt); got != tc.want {
				t.Errorf("reloaded prompt = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestSetSystemPrompt_Boundary pins the inclusive byte bound from both sides, so
// an off-by-one in either direction reddens.
func TestSetSystemPrompt_Boundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		size     int
		wantType string
	}{
		{"exactly at the bound is accepted", conversations.MaxSystemPromptBytes, protocol.TypeConversationUpdated},
		{"one byte over is refused", conversations.MaxSystemPromptBytes + 1, protocol.TypeError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			value := strings.Repeat("a", tc.size)
			reg, regPath := newSSPReg(t)
			c, recv := newSSPConn(t)

			h := SetSystemPrompt(reg, regPath, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
			req := sspRequest(t, protocol.SetSystemPromptPayload{
				ConversationID: sspTargetID,
				SystemPrompt:   &value,
			})
			if err := h(context.Background(), c, req); err != nil {
				t.Fatalf("handler: %v", err)
			}
			assertSSPEnvelopeShape(t, recv(), tc.wantType)

			cv, ok := reg.Get(conversations.ConversationID(sspTargetID))
			if !ok {
				t.Fatalf("target row vanished")
			}
			want := "text:" + value
			if tc.wantType == protocol.TypeError {
				want = "text:" + sspSeeded
			}
			if got := promptState(cv.SystemPrompt); got != want {
				t.Errorf("stored prompt for a %d-byte value: got %d bytes, want %d",
					tc.size, len(got), len(want))
			}
		})
	}
}

// TestSetSystemPrompt_Rejects covers AC #2: every reject is non-retryable,
// carries a fixed static message, and persists nothing — in memory or on disk.
func TestSetSystemPrompt_Rejects(t *testing.T) {
	t.Parallel()
	// malformedPayload decodes conversation_id successfully and only THEN fails on
	// system_prompt's type, so p.ConversationID holds supplied bytes at the moment
	// of the failure — the partially-populated-struct hazard the sibling verbs
	// document, and the reason the malformed branch logs neither the id nor err.
	malformedPayload := []byte(`{"conversation_id":"` + sspTargetID + `","system_prompt":12345}`)

	tests := []struct {
		name     string
		payload  []byte
		wantCode string
		wantMsg  string
	}{
		{"malformed payload", malformedPayload, protocol.CodeProtocolMalformed, msgSetSystemPromptMalformed},
		{
			"over-length value",
			[]byte(`{"conversation_id":"` + sspTargetID + `","system_prompt":"` + strings.Repeat("a", conversations.MaxSystemPromptBytes+1) + `"}`),
			protocol.CodeProtocolMalformed, msgSetSystemPromptTooLong,
		},
		{
			"unknown conversation",
			[]byte(`{"conversation_id":"conv-does-not-exist","system_prompt":"` + sspPrompt + `"}`),
			protocol.CodeConversationNotFound, msgSetSystemPromptNotFound,
		},
		// The fourth reject branch, invalid UTF-8, is deliberately absent from this
		// wire-driven table because no wire payload can reach it — see
		// TestSetSystemPrompt_InvalidUTF8IsUnreachableFromTheWire and the sentinel
		// injection in TestSetSystemPrompt_MapsRegistrySentinels.
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, regPath := newSSPReg(t)
			before := snapshotRows(t, reg)
			diskBefore, err := os.ReadFile(regPath)
			if err != nil {
				t.Fatalf("read seeded registry: %v", err)
			}
			c, recv := newSSPConn(t)

			h := SetSystemPrompt(reg, regPath, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
			if err := h(context.Background(), c, sspRawRequest(tc.payload)); err != nil {
				t.Fatalf("handler: %v", err)
			}

			env := assertSSPEnvelopeShape(t, recv(), protocol.TypeError)
			payload := assertErrorPayload(t, env, tc.wantCode, tc.wantMsg)
			if payload.Retryable {
				t.Errorf("Retryable = true, want false (re-issuing the same frame fails identically)")
			}

			// Nothing persisted: no row changed at all, and the on-disk file is
			// byte-identical (the handler must not even reach its eager Save).
			assertOnlyPromptChanged(t, before, snapshotRows(t, reg), "")
			diskAfter, err := os.ReadFile(regPath)
			if err != nil {
				t.Fatalf("read registry after reject: %v", err)
			}
			if !bytes.Equal(diskBefore, diskAfter) {
				t.Errorf("on-disk registry changed on a reject path")
			}
		})
	}
}

// TestSetSystemPrompt_NeverLogsSuppliedBytes covers AC #3's log half — the
// security-critical test. No supplied byte reaches the daemon log on ANY path,
// the success path included: not the prompt, not the conversation id, not the
// decode error. This is stricter than the change_workspace and
// archive_conversation templates, which both log conversation_id as a structured
// field on their non-malformed branches.
//
// Each case asserts its expected event record EXISTS first. Absence-only would be
// green against an empty buffer, which is the vacuous way to pass a leak test.
func TestSetSystemPrompt_NeverLogsSuppliedBytes(t *testing.T) {
	t.Parallel()
	valid := []byte(`{"conversation_id":"` + sspTargetID + `","system_prompt":"` + sspPrompt + `"}`)
	tests := []struct {
		name      string
		payload   []byte
		inject    error
		wantEvent string
	}{
		{"success", valid, nil, "set_system_prompt.applied"},
		{
			"malformed",
			[]byte(`{"conversation_id":"` + sspTargetID + `","system_prompt":12345}`),
			nil, "set_system_prompt.malformed",
		},
		{
			"too long",
			[]byte(`{"conversation_id":"` + sspTargetID + `","system_prompt":"` + strings.Repeat("a", conversations.MaxSystemPromptBytes+1) + `"}`),
			nil, "set_system_prompt.too_long",
		},
		// Injected rather than sent: no wire payload can produce an invalid-UTF-8
		// value (see TestSetSystemPrompt_InvalidUTF8IsUnreachableFromTheWire), so a
		// payload here would exercise the success branch and prove nothing about
		// this one.
		{"invalid utf8", valid, conversations.ErrSystemPromptInvalidUTF8, "set_system_prompt.invalid_utf8"},
		{
			"not found",
			[]byte(`{"conversation_id":"` + sspTargetID + `-absent","system_prompt":"` + sspPrompt + `"}`),
			nil, "set_system_prompt.not_found",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))
			reg, regPath := newSSPReg(t)
			c, recv := newSSPConn(t)

			h := SetSystemPrompt(&recordingSetter{reg: reg, err: tc.inject}, regPath, logger)
			if err := h(context.Background(), c, sspRawRequest(tc.payload)); err != nil {
				t.Fatalf("handler: %v", err)
			}
			recv()

			logged := buf.String()
			// Non-vacuity witness: the branch really did log, and it logged the one
			// daemon-minted field it is allowed to.
			rec := findLogRecord(t, logged, tc.wantEvent)
			if got := rec["conn_id"]; got != sspConnID {
				t.Errorf("conn_id = %v, want %q", got, sspConnID)
			}
			// Only then is "the supplied bytes are absent" meaningful.
			if strings.Contains(logged, sspPrompt) {
				t.Errorf("log leaked the prompt: %s", logged)
			}
			if strings.Contains(logged, sspTargetID) {
				t.Errorf("log leaked the conversation id: %s", logged)
			}
			if _, ok := rec["err"]; ok {
				t.Errorf("record carries an \"err\" field (may embed offending input bytes): %v", rec)
			}
			for _, banned := range []string{"system_prompt", "prompt", "conversation_id", "value", "len"} {
				if _, ok := rec[banned]; ok {
					t.Errorf("record carries a %q field, which can only hold supplied bytes: %v", banned, rec)
				}
			}
		})
	}
}

// recordingSetter is a ConversationSystemPromptSetter that records every call it
// receives, wrapping a real registry so behaviour is unchanged.
type recordingSetter struct {
	reg   *conversations.Registry
	calls []string
	err   error
}

func (r *recordingSetter) SetSystemPrompt(id conversations.ConversationID, prompt *string) error {
	r.calls = append(r.calls, "SetSystemPrompt")
	if r.err != nil {
		return r.err
	}
	return r.reg.SetSystemPrompt(id, prompt)
}

func (r *recordingSetter) Get(id conversations.ConversationID) (conversations.Conversation, bool) {
	r.calls = append(r.calls, "Get")
	return r.reg.Get(id)
}

func (r *recordingSetter) Save(path string) error {
	r.calls = append(r.calls, "Save")
	return r.reg.Save(path)
}

func (r *recordingSetter) WorkspaceLabel(cwd string) (string, bool) {
	r.calls = append(r.calls, "WorkspaceLabel")
	return r.reg.WorkspaceLabel(cwd)
}

// TestSetSystemPrompt_TouchesNoSessionSurface covers AC #4. The handler cannot
// restart, rotate, recompose the argv of, or interrupt a live session, because
// its entire interaction with the daemon is four conversations-registry calls —
// there is no session, pool or runner seam in its dependency set to reach.
//
// The fourth is WorkspaceLabel, added by #2210 to fill the reply's
// workspace_label. It moved this count from three deliberately: the sequence is
// the assertion, so a new registry door has to be admitted here rather than
// absorbed. It is still a conversations-registry read and still reaches no
// session surface, which is what this test is about.
// (The structural half of that proof is the constructor signature, which this
// test compiles against; the observable half is the exact call sequence below.)
//
// The value's route to a running session's NEXT start is #2150's
// refreshSystemPrompt, called from Pool.Activate, which re-reads exactly the
// registry field asserted here.
func TestSetSystemPrompt_TouchesNoSessionSurface(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		payload   []byte
		wantCalls []string
	}{
		{
			"success writes, snapshots, persists — and nothing more",
			[]byte(`{"conversation_id":"` + sspTargetID + `","system_prompt":"` + sspPrompt + `"}`),
			[]string{"SetSystemPrompt", "Get", "Save", "WorkspaceLabel"},
		},
		{
			"a refused write never reaches the snapshot or the persist",
			[]byte(`{"conversation_id":"conv-does-not-exist","system_prompt":"` + sspPrompt + `"}`),
			[]string{"SetSystemPrompt"},
		},
		{
			"a malformed frame touches the registry not at all",
			[]byte(`{"conversation_id":"` + sspTargetID + `","system_prompt":12345}`),
			nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, regPath := newSSPReg(t)
			rec := &recordingSetter{reg: reg}
			c, recv := newSSPConn(t)

			h := SetSystemPrompt(rec, regPath, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
			if err := h(context.Background(), c, sspRawRequest(tc.payload)); err != nil {
				t.Fatalf("handler: %v", err)
			}
			recv()

			if !reflect.DeepEqual(rec.calls, tc.wantCalls) {
				t.Errorf("registry calls = %v, want %v", rec.calls, tc.wantCalls)
			}
		})
	}
}

// errUnknownRegistryFailure stands in for a sentinel SetSystemPrompt does not
// return today — a fourth refusal a future change to the registry could add.
var errUnknownRegistryFailure = errors.New("registry: some future refusal")

// TestSetSystemPrompt_MapsRegistrySentinels pins the whole sentinel → wire-code
// table by INJECTING each error, which is the only way to exercise two of these
// rows: invalid UTF-8 is unreachable from the wire (see the test below), and the
// unknown-error row is by definition not producible by today's registry.
//
// The unknown row is the load-bearing one: without a default arm, an unrecognised
// error would fall through to the snapshot-and-reply path and ack a write that
// never happened. It must fail closed instead.
func TestSetSystemPrompt_MapsRegistrySentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		inject   error
		wantCode string
		wantMsg  string
	}{
		{"too long", conversations.ErrSystemPromptTooLong, protocol.CodeProtocolMalformed, msgSetSystemPromptTooLong},
		{"invalid utf8", conversations.ErrSystemPromptInvalidUTF8, protocol.CodeProtocolMalformed, msgSetSystemPromptInvalidUTF8},
		{"not found", conversations.ErrConversationNotFound, protocol.CodeConversationNotFound, msgSetSystemPromptNotFound},
		{"unknown error fails closed", errUnknownRegistryFailure, protocol.CodeProtocolMalformed, msgSetSystemPromptMalformed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, regPath := newSSPReg(t)
			rec := &recordingSetter{reg: reg, err: tc.inject}
			c, recv := newSSPConn(t)

			h := SetSystemPrompt(rec, regPath, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
			req := sspRequest(t, protocol.SetSystemPromptPayload{
				ConversationID: sspTargetID,
				SystemPrompt:   strptr(sspPrompt),
			})
			if err := h(context.Background(), c, req); err != nil {
				t.Fatalf("handler: %v", err)
			}

			env := assertSSPEnvelopeShape(t, recv(), protocol.TypeError)
			payload := assertErrorPayload(t, env, tc.wantCode, tc.wantMsg)
			if payload.Retryable {
				t.Errorf("Retryable = true, want false")
			}
			// A refused write never reaches the reply snapshot or the eager persist.
			if !reflect.DeepEqual(rec.calls, []string{"SetSystemPrompt"}) {
				t.Errorf("registry calls = %v, want only the refused write", rec.calls)
			}
		})
	}
}

// TestSetSystemPrompt_InvalidUTF8IsUnreachableFromTheWire records WHY the
// invalid-UTF-8 branch is pinned by injection rather than by a payload, so a
// later reader does not "fix" the gap by adding a wire case that would silently
// exercise the success path instead.
//
// encoding/json substitutes U+FFFD for every invalid byte and every unpaired
// surrogate while decoding a string, so the value the handler hands the registry
// is ALWAYS valid UTF-8 no matter what arrives on the wire. The registry's check
// is therefore defence in depth for its non-wire callers, and the handler maps
// the sentinel because mapping it is free and failing to map it would be a
// fall-through.
//
// This also means the substitution happens BEFORE #2149's validation rather than
// after it, so the round-trip guarantee the sentinel protects is not at risk on
// this path: what gets length-checked is what gets stored.
func TestSetSystemPrompt_InvalidUTF8IsUnreachableFromTheWire(t *testing.T) {
	t.Parallel()
	payloads := map[string][]byte{
		"raw invalid bytes":         []byte("{\"conversation_id\":\"" + sspTargetID + "\",\"system_prompt\":\"bad \xed\xa0\x80 bytes\"}"),
		"unpaired surrogate escape": []byte(`{"conversation_id":"` + sspTargetID + `","system_prompt":"bad \ud800 bytes"}`),
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var p protocol.SetSystemPromptPayload
			if err := json.Unmarshal(payload, &p); err != nil {
				t.Fatalf("payload did not decode (it must reach the registry to make the point): %v", err)
			}
			if p.SystemPrompt == nil {
				t.Fatalf("system_prompt decoded to nil")
			}
			if !utf8.ValidString(*p.SystemPrompt) {
				t.Fatalf("decoded value is invalid UTF-8 — the wire CAN reach the branch, so it needs a wire-driven reject case")
			}

			// And end to end: such a frame is accepted, not refused.
			reg, regPath := newSSPReg(t)
			c, recv := newSSPConn(t)
			h := SetSystemPrompt(reg, regPath, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
			if err := h(context.Background(), c, sspRawRequest(payload)); err != nil {
				t.Fatalf("handler: %v", err)
			}
			assertSSPEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)
		})
	}
}

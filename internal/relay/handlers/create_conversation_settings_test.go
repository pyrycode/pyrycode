package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// createConvSettings runs one create_conversation carrying p on a multi_agent
// conn over creator, and returns the reply's envelope, the registry's row count
// and everything the handler logged.
func createConvSettings(t *testing.T, creator *stubSessionCreator, p protocol.CreateConversationPayload, wantType string) (protocol.Envelope, int, string) {
	t.Helper()
	reg, regPath := newCreateConvReg(t)
	c, recv := newCreateConvConn(t)
	c.SetMultiAgent(true)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := CreateConversation(reg, creator, regPath, createConvDefault, logger)
	if err := h(context.Background(), c, createConvRequest(t, p)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	return assertCreateConvEnvelopeShape(t, recv(), wantType), len(reg.List()), logs.String()
}

// TestCreateConversation_Settings_Refused (#2665): a refused model or effort gets
// set_session_settings' code and retryable flag for that value, creates no row,
// and is never echoed or logged. A shape refusal never reaches the creator; a
// membership refusal is the creator's, returned before it minted anything.
func TestCreateConversation_Settings_Refused(t *testing.T) {
	t.Parallel()
	codex := protocol.AgentCodex
	badModel, badEffort := "-Secret-Model", "Secret Effort"
	model, effort := "secret-model", "secret-effort"
	cases := []struct {
		name        string
		model       *string
		effort      *string
		creatorErr  error
		wantCalls   int
		wantCode    string
		wantMsg     string
		wantRetry   bool
		wantInReply string // a value that must appear neither in the reply nor the log
	}{
		{"model shape", &badModel, nil, nil, 0, protocol.CodeProtocolMalformed, msgCreateConversationMalformed, false, badModel},
		{"effort shape", nil, &badEffort, nil, 0, protocol.CodeProtocolMalformed, msgCreateConversationMalformed, false, badEffort},
		{"model not offered", &model, nil, relay.ErrModelNotOffered, 1, protocol.CodeProtocolMalformed, relay.MsgSettingsModelNotOffered, false, model},
		{"effort not offered", &model, &effort, fmt.Errorf("wrapped: %w", relay.ErrEffortNotOffered), 1, protocol.CodeProtocolMalformed, msgCreateConversationMalformed, false, effort},
		{"vocabulary unavailable", &model, nil, relay.ErrModelVocabularyUnavailable, 1, protocol.CodeModelListUnavailable, relay.MsgModelListUnavailable, true, model},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			creator := &stubSessionCreator{err: tc.creatorErr}
			env, rows, logs := createConvSettings(t, creator, protocol.CreateConversationPayload{Agent: &codex, Model: tc.model, Effort: tc.effort}, protocol.TypeError)
			var payload protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &payload); err != nil {
				t.Fatalf("unmarshal error payload: %v", err)
			}
			if payload.Code != tc.wantCode || payload.Message != tc.wantMsg || payload.Retryable != tc.wantRetry {
				t.Errorf("error = %+v, want code %q message %q retryable %v", payload, tc.wantCode, tc.wantMsg, tc.wantRetry)
			}
			if creator.calls != tc.wantCalls {
				t.Errorf("creator calls = %d, want %d", creator.calls, tc.wantCalls)
			}
			if rows != 0 {
				t.Errorf("registry rows = %d, want 0", rows)
			}
			if strings.Contains(string(env.Payload), tc.wantInReply) || strings.Contains(logs, tc.wantInReply) {
				t.Errorf("requested value %q echoed; reply %s log %s", tc.wantInReply, env.Payload, logs)
			}
		})
	}
}

// TestCreateConversation_Settings_Forwarded (#2665): an accepted create hands the
// creator exactly the requested pointers — nil for an absent field, so the
// creator keeps today's value for it — and the reply shape is unchanged.
func TestCreateConversation_Settings_Forwarded(t *testing.T) {
	t.Parallel()
	model, effort, empty := "luna", "ultra", ""
	cases := []struct {
		name          string
		model, effort *string
	}{
		{"neither", nil, nil},
		{"both", &model, &effort},
		{"model only", &model, nil},
		{"effort only", nil, &effort},
		{"explicit empty model", &empty, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			creator := &stubSessionCreator{}
			env, rows, _ := createConvSettings(t, creator, protocol.CreateConversationPayload{Model: tc.model, Effort: tc.effort}, protocol.TypeConversationCreated)
			if rows != 1 || len(creator.settings) != 1 {
				t.Fatalf("rows %d, creator calls %d; want 1 and 1", rows, len(creator.settings))
			}
			got := creator.settings[0]
			if !sameStringPtr(got.Model, tc.model) || !sameStringPtr(got.Effort, tc.effort) {
				t.Errorf("creator settings = %s/%s, want %s/%s", ptrString(got.Model), ptrString(got.Effort), ptrString(tc.model), ptrString(tc.effort))
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(env.Payload, &raw); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			for _, key := range []string{"model", "effort"} {
				if _, present := raw[key]; present {
					t.Errorf("reply carries %q, want the conversation_created shape unchanged", key)
				}
			}
		})
	}
}

// TestCreateConversationPayload_SettingsOmitted (#2665): a request with neither
// field encodes exactly as it did before they existed.
func TestCreateConversationPayload_SettingsOmitted(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(protocol.CreateConversationPayload{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"is_promoted":null,"name":null,"cwd":null}`; string(got) != want {
		t.Errorf("encoding = %s, want %s", got, want)
	}
	model, effort := "luna", "ultra"
	got, err = json.Marshal(protocol.CreateConversationPayload{Model: &model, Effort: &effort})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back protocol.CreateConversationPayload
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ptrString(back.Model) != model || ptrString(back.Effort) != effort {
		t.Errorf("round trip = %s/%s, want %s/%s", ptrString(back.Model), ptrString(back.Effort), model, effort)
	}
}

func sameStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func ptrString(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

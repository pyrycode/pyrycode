package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

type hostPromptStore struct {
	current string
	err     error
	writes  int
}

func (s *hostPromptStore) DaemonInstructions() string      { return s.current }
func (*hostPromptStore) DefaultDaemonInstructions() string { return "factory-default" }
func (s *hostPromptStore) SetDaemonInstructions(text string) error {
	s.writes++
	if s.err != nil {
		return s.err
	}
	if len(text) > conversations.MaxSystemPromptBytes {
		return sessions.ErrDaemonInstructionsTooLong
	}
	s.current = text
	return nil
}

func TestHostSystemPromptHandlers(t *testing.T) {
	t.Parallel()
	const secret = "HOST-PROMPT-PRIVATE-SENTINEL-2768"
	writePayload := func(text string) string {
		b, err := json.Marshal(protocol.SetHostSystemPromptPayload{SystemPrompt: &text})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cases := []struct {
		name, payload string
		read          bool
		injected      error
		code          string
		retry         bool
		want          string
		writes        int
	}{
		{name: "read", read: true, payload: `{}`, want: "prior"},
		{name: "read malformed", read: true, payload: `{"` + secret, code: protocol.CodeProtocolMalformed},
		{name: "read wrong type", read: true, payload: `[]`, code: protocol.CodeProtocolMalformed},
		{name: "write malformed", payload: `{"system_prompt":"` + secret, code: protocol.CodeProtocolMalformed},
		{name: "missing", payload: `{}`, code: protocol.CodeProtocolMalformed},
		{name: "null", payload: `{"system_prompt":null}`, code: protocol.CodeProtocolMalformed},
		{name: "number", payload: `{"system_prompt":42}`, code: protocol.CodeProtocolMalformed},
		{name: "object", payload: `{"system_prompt":{}}`, code: protocol.CodeProtocolMalformed},
		{name: "verbatim", payload: writePayload(" \t" + secret + "\r\n规则\n"), want: " \t" + secret + "\r\n规则\n", writes: 1},
		{name: "empty", payload: writePayload(""), writes: 1},
		{name: "reset", payload: writePayload("factory-default"), want: "factory-default", writes: 1},
		{name: "inclusive bytes", payload: writePayload(strings.Repeat("ä", conversations.MaxSystemPromptBytes/2)), want: strings.Repeat("ä", conversations.MaxSystemPromptBytes/2), writes: 1},
		{name: "oversized", payload: writePayload(strings.Repeat("x", conversations.MaxSystemPromptBytes+1)), code: protocol.CodeProtocolMalformed, writes: 1},
		{name: "wrapped length", payload: writePayload(secret), injected: fmt.Errorf("%s: %w", secret, sessions.ErrDaemonInstructionsTooLong), code: protocol.CodeProtocolMalformed, writes: 1},
		{name: "wrapped UTF8", payload: writePayload(secret), injected: fmt.Errorf("%s: %w", secret, sessions.ErrDaemonInstructionsInvalidUTF8), code: protocol.CodeProtocolMalformed, writes: 1},
		{name: "persistence", payload: writePayload(secret), injected: fmt.Errorf("storage failed: %s", secret), code: protocol.CodeHostSystemPromptUnavailable, retry: true, writes: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &hostPromptStore{current: "prior", err: tc.injected}
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			out := make(chan protocol.RoutingEnvelope, 4)
			conn := dispatch.NewTestConn("requester", out, nil)
			h := SetHostSystemPrompt(store, logger)
			verb := protocol.TypeSetHostSystemPrompt
			if tc.read {
				h = RequestHostSystemPrompt(store, logger)
				verb = protocol.TypeRequestHostSystemPrompt
			}
			req := protocol.Envelope{ID: 2768, Type: verb, Payload: json.RawMessage(tc.payload)}
			if err := h(context.Background(), conn, req); err != nil {
				t.Fatal(err)
			}
			if len(out) != 1 {
				t.Fatalf("reply count = %d", len(out))
			}
			routed := <-out
			var env protocol.Envelope
			if err := json.Unmarshal(routed.Frame, &env); err != nil {
				t.Fatal(err)
			}
			if routed.ConnID != "requester" || env.InReplyTo == nil || *env.InReplyTo != req.ID {
				t.Fatal("reply is not requester-correlated")
			}
			if store.writes != tc.writes {
				t.Fatalf("setter calls = %d, want %d", store.writes, tc.writes)
			}
			if tc.code != "" {
				var p protocol.ErrorPayload
				if err := json.Unmarshal(env.Payload, &p); err != nil {
					t.Fatal(err)
				}
				if env.Type != protocol.TypeError || p.Code != tc.code || p.Retryable != tc.retry {
					t.Fatalf("error reply = %s / %+v", env.Type, p)
				}
				if store.current != "prior" {
					t.Fatal("refusal changed memory")
				}
				if strings.Contains(string(env.Payload), secret) {
					t.Fatal("error exposed prompt")
				}
			} else {
				var p protocol.HostSystemPromptPayload
				if err := json.Unmarshal(env.Payload, &p); err != nil {
					t.Fatal(err)
				}
				want := protocol.HostSystemPromptPayload{SystemPrompt: tc.want, DefaultSystemPrompt: "factory-default"}
				if env.Type != protocol.TypeHostSystemPrompt || p != want {
					t.Fatal("success pair differs")
				}
				var keys map[string]json.RawMessage
				if err := json.Unmarshal(env.Payload, &keys); err != nil {
					t.Fatal(err)
				}
				if len(keys) != 2 || keys["system_prompt"] == nil || keys["default_system_prompt"] == nil {
					t.Fatal("reply omitted a required key")
				}
			}
			if strings.Contains(logs.String(), secret) {
				t.Fatal("logs exposed prompt")
			}
		})
	}
}

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type fixedClaudeAccount protocol.ClaudeAccountPayload

func (f fixedClaudeAccount) ClaudeAccount() protocol.ClaudeAccountPayload {
	return protocol.ClaudeAccountPayload(f)
}

func TestRequestClaudeAccount(t *testing.T) {
	t.Parallel()
	const secret = "CLAUDE-ACCOUNT-PAYLOAD-SENTINEL-2839"
	src := fixedClaudeAccount{Kind: protocol.ClaudeAccountKindFile, Label: "work", State: protocol.ClaudeAccountStateFailed, Reason: "token file missing"}
	cases := []struct {
		name, payload string
		malformed     bool
	}{
		{name: "empty object", payload: `{}`},
		{name: "unknown key ignored", payload: `{"extra":1}`},
		{name: "truncated", payload: `{"` + secret, malformed: true},
		{name: "wrong type", payload: `["` + secret + `"]`, malformed: true},
		{name: "absent", payload: ``, malformed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			out := make(chan protocol.RoutingEnvelope, 4)
			conn := dispatch.NewTestConn("requester", out, nil)
			req := protocol.Envelope{ID: 2839, Type: protocol.TypeRequestClaudeAccount, Payload: json.RawMessage(tc.payload)}
			if err := RequestClaudeAccount(src, logger)(context.Background(), conn, req); err != nil {
				t.Fatal(err)
			}
			if len(out) != 1 {
				t.Fatalf("reply count = %d, want 1", len(out))
			}
			routed := <-out
			var env protocol.Envelope
			if err := json.Unmarshal(routed.Frame, &env); err != nil {
				t.Fatal(err)
			}
			if routed.ConnID != "requester" || env.InReplyTo == nil || *env.InReplyTo != req.ID {
				t.Fatal("reply is not requester-correlated")
			}
			if tc.malformed {
				var p protocol.ErrorPayload
				if err := json.Unmarshal(env.Payload, &p); err != nil {
					t.Fatal(err)
				}
				if env.Type != protocol.TypeError || p.Code != protocol.CodeProtocolMalformed || p.Retryable {
					t.Fatalf("error reply = %s / %+v", env.Type, p)
				}
			} else {
				var p protocol.ClaudeAccountPayload
				if err := json.Unmarshal(env.Payload, &p); err != nil {
					t.Fatal(err)
				}
				if env.Type != protocol.TypeClaudeAccount || p != protocol.ClaudeAccountPayload(src) {
					t.Fatalf("reply = %s / %+v", env.Type, p)
				}
				var keys map[string]json.RawMessage
				if err := json.Unmarshal(env.Payload, &keys); err != nil {
					t.Fatal(err)
				}
				if len(keys) != 4 {
					t.Fatalf("reply keys = %v, want kind, label, state, reason", keys)
				}
			}
			if strings.Contains(string(routed.Frame), secret) || strings.Contains(logs.String(), secret) {
				t.Fatal("request payload echoed")
			}
		})
	}
}

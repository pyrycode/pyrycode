package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type fixedClaudeUsage string

func (f fixedClaudeUsage) ClaudeUsage() protocol.ClaudeUsagePayload {
	var p protocol.ClaudeUsagePayload
	if err := json.Unmarshal([]byte(f), &p); err != nil {
		panic(err)
	}
	return p
}

func TestRequestClaudeUsage(t *testing.T) {
	t.Parallel()
	const observed = `{"state":"observed","observed_at":"2026-10-17T12:00:00Z","five_hour":{"utilization":0,"resets_at":1000}}`
	cases := []struct {
		name, payload string
		src           ClaudeUsageSource
		want          string // reply payload; "" means a malformed-error reply
	}{
		{name: "observed reading", payload: `{}`, src: fixedClaudeUsage(observed), want: observed},
		{name: "unknown key ignored", payload: `{"extra":1}`, src: fixedClaudeUsage(observed), want: observed},
		{name: "no reading yet", payload: `{}`, src: fixedClaudeUsage(`{"state":"none"}`), want: `{"state":"none"}`},
		{name: "nil source answers none", payload: `{}`, want: `{"state":"none"}`},
		{name: "wrong type", payload: `["x"]`, src: fixedClaudeUsage(observed)},
		{name: "absent", payload: ``, src: fixedClaudeUsage(observed)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			out := make(chan protocol.RoutingEnvelope, 4)
			conn := dispatch.NewTestConn("requester", out, nil)
			req := protocol.Envelope{ID: 3575, Type: protocol.TypeRequestClaudeUsage, Payload: json.RawMessage(tc.payload)}
			if err := RequestClaudeUsage(tc.src, logger)(context.Background(), conn, req); err != nil {
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
			if tc.want == "" {
				var p protocol.ErrorPayload
				if err := json.Unmarshal(env.Payload, &p); err != nil {
					t.Fatal(err)
				}
				if env.Type != protocol.TypeError || p.Code != protocol.CodeProtocolMalformed || p.Retryable {
					t.Fatalf("error reply = %s / %+v", env.Type, p)
				}
				return
			}
			if env.Type != protocol.TypeClaudeUsage || string(env.Payload) != tc.want {
				t.Fatalf("reply = %s / %s, want %s", env.Type, env.Payload, tc.want)
			}
		})
	}
}

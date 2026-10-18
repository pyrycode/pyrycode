package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type suggestionResetRouter struct{}

func (suggestionResetRouter) Route(string) (handlers.TurnWriter, error) { return nil, nil }

func TestReplySuggestions_ResetAliases(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"/clear", "/reset"} {
		for _, refused := range []bool{false, true} {
			name := command + "/success"
			if refused {
				name = command + "/refused"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				e, s := newSuggestionEmitter(testConvID, suggestConvB)
				defer s.stopFallbacks()
				ctx := context.Background()
				for _, id := range []string{testConvID, suggestConvB} {
					suggestedTurn(e, s, id)
					e.HandleFor(ctx, id, turnevent.PromptSuggestion{Text: suggestText})
				}
				inner := stubResetter{}
				if refused {
					inner.err = errors.New("refused")
				}
				reset := suggestionResetter{inner: inner, s: s}
				out := make(chan protocol.RoutingEnvelope, 2)
				conn := dispatch.NewTestConn("reset-suggestion", out, nil)
				payload, err := json.Marshal(protocol.SendMessagePayload{ConversationID: testConvID, Text: command})
				if err != nil {
					t.Fatal(err)
				}
				// A mistakenly enqueued alias must not invalidate via acceptance.
				q := suggestionEnqueuer{inner: stubEnqueuer{id: 0}, s: s}
				h := handlers.SendMessage(suggestionResetRouter{}, q, nil, nil, "", nil, reset, discardLogger())
				if err := h(ctx, conn, protocol.Envelope{ID: 1, Type: protocol.TypeSendMessage, Payload: payload}); err != nil {
					t.Fatal(err)
				}
				if len(out) != 1 {
					t.Fatalf("reply count = %d, want 1", len(out))
				}
				var reply protocol.Envelope
				if err := json.Unmarshal((<-out).Frame, &reply); err != nil || reply.Type != protocol.TypeAck {
					t.Fatalf("reply = %+v, err = %v; want ack", reply, err)
				}
				p, ok := suggestionFor(t, s, testConvID)
				if !ok || (refused && (p.SuggestedReply == nil || *p.SuggestedReply != suggestText || p.Revision != 1)) ||
					(!refused && (p.SuggestedReply != nil || p.Revision != 2)) {
					t.Errorf("suggestion after refused=%v reset = %+v, present=%v", refused, p, ok)
				}
				other, ok := suggestionFor(t, s, suggestConvB)
				if !ok || other.SuggestedReply == nil || *other.SuggestedReply != suggestText || other.Revision != 1 {
					t.Errorf("other conversation changed: %+v, present=%v", other, ok)
				}
			})
		}
	}
}

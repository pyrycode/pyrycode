package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const (
	suggestConvB     = "22222222-2222-4222-8222-222222222222"
	suggestText      = "Run the tests next"
	suggestSessionID = "33333333-3333-4333-8333-333333333333"
)

var successEnd = turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn, Outcome: "success"}

// newSuggestionEmitter wires an emitter to a fresh owner whose resolver knows
// every conversation id in live.
func newSuggestionEmitter(live ...string) (*interactiveTurnEmitterV2, *replySuggestions) {
	s := newReplySuggestions(discardLogger())
	s.bindSessions(func(convID string) (string, bool) {
		for _, id := range live {
			if id == convID {
				return suggestSessionID, true
			}
		}
		return "", false
	})
	e := newInteractiveTurnEmitterV2(&stubCursor{}, &fakeInteractiveBcast{}, discardLogger())
	e.suggestions = s
	return e, s
}

// suggestedTurn drives one delivered, answered, successfully ended turn.
func suggestedTurn(e *interactiveTurnEmitterV2, s *replySuggestions, convID string) {
	ctx := context.Background()
	s.noteDelivered(convID, msgqueue.QueuedMessage{Text: "what next?"})
	e.HandleFor(ctx, convID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
	e.HandleFor(ctx, convID, successEnd)
}

func suggestionFor(t *testing.T, s *replySuggestions, convID string) (protocol.ReplySuggestionPayload, bool) {
	t.Helper()
	for _, p := range s.current() {
		if p.ConversationID == convID {
			return p, true
		}
	}
	return protocol.ReplySuggestionPayload{}, false
}

func TestReplySuggestions_SetConditions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tests := []struct {
		name  string
		drive func(e *interactiveTurnEmitterV2, s *replySuggestions)
		want  bool
	}{
		{"eligible turn", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			suggestedTurn(e, s, testConvID)
		}, true},
		{"no delivered user text", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
			e.HandleFor(ctx, testConvID, successEnd)
		}, false},
		{"blank delivered user text", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			s.noteDelivered(testConvID, msgqueue.QueuedMessage{Text: " \n"})
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
			e.HandleFor(ctx, testConvID, successEnd)
		}, false},
		{"subagent text only", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			s.noteDelivered(testConvID, msgqueue.QueuedMessage{Text: "go"})
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "child", ParentToolCallID: "toolu_1"})
			e.HandleFor(ctx, testConvID, successEnd)
		}, false},
		{"error outcome", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			s.noteDelivered(testConvID, msgqueue.QueuedMessage{Text: "go"})
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
			e.HandleFor(ctx, testConvID, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn, Outcome: "error_max_turns"})
		}, false},
		{"is_error", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			s.noteDelivered(testConvID, msgqueue.QueuedMessage{Text: "go"})
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
			e.HandleFor(ctx, testConvID, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn, Outcome: "success", IsError: true})
		}, false},
		{"cancelled", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			s.noteDelivered(testConvID, msgqueue.QueuedMessage{Text: "go"})
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
			e.HandleFor(ctx, testConvID, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonCancelled, Outcome: "success"})
		}, false},
		{"invalidated mid-turn", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			s.noteDelivered(testConvID, msgqueue.QueuedMessage{Text: "go"})
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
			s.invalidate(testConvID)
			e.HandleFor(ctx, testConvID, successEnd)
		}, false},
		{"invalidated after turn end", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			suggestedTurn(e, s, testConvID)
			s.invalidate(testConvID)
		}, false},
		{"exit after turn end", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			suggestedTurn(e, s, testConvID)
			e.closeForConversation(ctx, testConvID)
		}, false},
		{"new turn before the suggestion", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			suggestedTurn(e, s, testConvID)
			e.HandleFor(ctx, testConvID, turnevent.ThoughtChunk{Text: "hmm"})
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, s := newSuggestionEmitter(testConvID)
			tt.drive(e, s)
			bcast := e.bcast.(*fakeInteractiveBcast)
			before, inTurn := len(bcast.pushes), e.inTurn
			e.HandleFor(ctx, testConvID, turnevent.PromptSuggestion{Text: suggestText})
			if got := len(bcast.pushes); got != before {
				t.Errorf("the suggestion arm emitted %d event frames, want none", got-before)
			}
			if e.inTurn != inTurn {
				t.Error("the suggestion arm changed the turn lifecycle")
			}
			p, ok := suggestionFor(t, s, testConvID)
			if got := ok && p.SuggestedReply != nil; got != tt.want {
				t.Fatalf("suggestion published = %v, want %v", got, tt.want)
			}
			if tt.want && (*p.SuggestedReply != suggestText || p.Revision != 1 || p.SessionID != suggestSessionID) {
				t.Errorf("payload = %+v, want text %q revision 1 session %q", p, suggestText, suggestSessionID)
			}
		})
	}
}

// stubEnqueuer and stubQueueSender answer a fixed acceptance.
type stubEnqueuer struct{ id uint64 }

func (q stubEnqueuer) EnqueueSent(string, string, string, string, []string, string, string, time.Time) uint64 {
	return q.id
}

type stubQueueSender struct{ ok bool }

func (q stubQueueSender) SendNow(string, uint64) bool { return q.ok }

type stubResetter struct{}

func (stubResetter) StartNewSession(string) error { return nil }

type stubTransitionSink struct{ fn sessions.TransitionObserver }

func (k *stubTransitionSink) SetTransitionObserver(fn sessions.TransitionObserver) { k.fn = fn }

func TestReplySuggestions_ClearTriggers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	resolve := func(sid string) (string, bool) { return testConvID, sid == suggestSessionID }
	transition := func(s *replySuggestions, reason sessions.TransitionReason) {
		k := &stubTransitionSink{}
		suggestionTransitionSink{inner: k, s: s, resolve: resolve}.SetTransitionObserver(func(sessions.SessionTransition) {})
		k.fn(sessions.SessionTransition{PreviousID: suggestSessionID, NewID: suggestSessionID, Reason: reason})
	}
	tests := []struct {
		name    string
		trigger func(e *interactiveTurnEmitterV2, s *replySuggestions)
		clears  bool
	}{
		{"accepted send", func(_ *interactiveTurnEmitterV2, s *replySuggestions) {
			suggestionEnqueuer{inner: stubEnqueuer{id: 7}, s: s}.EnqueueSent(testConvID, "m", "t", "t", nil, "", "", time.Time{})
		}, true},
		{"rejected send", func(_ *interactiveTurnEmitterV2, s *replySuggestions) {
			suggestionEnqueuer{inner: stubEnqueuer{id: 0}, s: s}.EnqueueSent(testConvID, "m", "t", "t", nil, "", "", time.Time{})
		}, false},
		{"accepted send-now", func(_ *interactiveTurnEmitterV2, s *replySuggestions) {
			suggestionQueueSender{inner: stubQueueSender{ok: true}, s: s}.SendNow(testConvID, 1)
		}, true},
		{"refused send-now", func(_ *interactiveTurnEmitterV2, s *replySuggestions) {
			suggestionQueueSender{inner: stubQueueSender{ok: false}, s: s}.SendNow(testConvID, 1)
		}, false},
		{"/clear", func(_ *interactiveTurnEmitterV2, s *replySuggestions) {
			_ = suggestionResetter{inner: stubResetter{}, s: s}.StartNewSession(testConvID)
		}, true},
		{"reset transition", func(_ *interactiveTurnEmitterV2, s *replySuggestions) {
			transition(s, sessions.ReasonClear)
		}, true},
		{"eviction transition", func(_ *interactiveTurnEmitterV2, s *replySuggestions) {
			transition(s, sessions.ReasonEviction)
		}, true},
		{"session exit", func(e *interactiveTurnEmitterV2, _ *replySuggestions) {
			e.closeForConversation(ctx, testConvID)
		}, true},
		{"new turn", func(e *interactiveTurnEmitterV2, _ *replySuggestions) {
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m2", Text: "more"})
		}, true},
		{"other conversation's send", func(_ *interactiveTurnEmitterV2, s *replySuggestions) {
			s.invalidate(suggestConvB)
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, s := newSuggestionEmitter(testConvID, suggestConvB)
			suggestedTurn(e, s, testConvID)
			e.HandleFor(ctx, testConvID, turnevent.PromptSuggestion{Text: suggestText})
			tt.trigger(e, s)
			p, ok := suggestionFor(t, s, testConvID)
			if !ok {
				t.Fatal("conversation has no suggestion state")
			}
			if tt.clears {
				if p.SuggestedReply != nil || p.Revision != 2 {
					t.Errorf("after trigger = %+v, want null at revision 2", p)
				}
			} else if p.SuggestedReply == nil || p.Revision != 1 {
				t.Errorf("after non-trigger = %+v, want the set at revision 1", p)
			}
			if _, ok := suggestionFor(t, s, suggestConvB); ok {
				t.Error("the other conversation gained suggestion state")
			}
			// The cleared turn cannot publish late.
			e.HandleFor(ctx, testConvID, turnevent.PromptSuggestion{Text: "late"})
			if p2, _ := suggestionFor(t, s, testConvID); tt.clears && p2 != p {
				t.Errorf("late suggestion changed state to %+v", p2)
			}
		})
	}
}

func TestReplySuggestions_PerConversationAndDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	live := map[string]bool{testConvID: true, suggestConvB: true}
	var mu sync.Mutex
	s := newReplySuggestions(discardLogger())
	s.bindSessions(func(convID string) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		return suggestSessionID, live[convID]
	})
	e := newInteractiveTurnEmitterV2(&stubCursor{}, &fakeInteractiveBcast{}, discardLogger())
	e.suggestions = s

	suggestedTurn(e, s, testConvID)
	e.HandleFor(ctx, testConvID, turnevent.PromptSuggestion{Text: suggestText})
	suggestedTurn(e, s, suggestConvB)
	e.HandleFor(ctx, suggestConvB, turnevent.PromptSuggestion{Text: "other"})
	s.invalidate(testConvID)

	got := s.current()
	if len(got) != 2 || got[0].ConversationID != testConvID || got[1].ConversationID != suggestConvB {
		t.Fatalf("current = %+v, want both conversations in id order", got)
	}
	if got[0].SuggestedReply != nil || got[0].Revision != 2 {
		t.Errorf("cleared conversation = %+v, want null at revision 2", got[0])
	}
	if got[1].SuggestedReply == nil || *got[1].SuggestedReply != "other" || got[1].Revision != 1 {
		t.Errorf("other conversation = %+v, want its own set at revision 1", got[1])
	}

	mu.Lock()
	delete(live, testConvID)
	mu.Unlock()
	if got := s.current(); len(got) != 1 || got[0].ConversationID != suggestConvB {
		t.Errorf("after delete current = %+v, want only the surviving conversation", got)
	}
}

// syncBcast is a goroutine-safe broadcaster for Run.
type syncBcast struct {
	conns  []relay.ActiveConn
	pushed chan recordedPush
}

func (b *syncBcast) ActiveConns(context.Context) []relay.ActiveConn { return b.conns }

func (b *syncBcast) Push(_ context.Context, connID string, env protocol.Envelope) error {
	b.pushed <- recordedPush{connID: connID, env: env}
	return nil
}

func TestReplySuggestions_RunFansOutToInteractiveConns(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &syncBcast{
		conns:  []relay.ActiveConn{{ConnID: "a", Interactive: true}, {ConnID: "plain"}, {ConnID: "b", Interactive: true}},
		pushed: make(chan recordedPush, 8),
	}
	e, s := newSuggestionEmitter(testConvID)
	stop := startReplySuggestionsV2(ctx, s, b)
	defer stop()
	defer cancel()

	suggestedTurn(e, s, testConvID)
	e.HandleFor(ctx, testConvID, turnevent.PromptSuggestion{Text: suggestText})

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case p := <-b.pushed:
			if p.connID == "plain" {
				t.Fatal("a non-interactive conn received reply_suggestion")
			}
			if p.env.Type != protocol.TypeReplySuggestion || p.env.EventID != nil {
				t.Fatalf("envelope = %+v, want reply_suggestion without an event id", p.env)
			}
			var got protocol.ReplySuggestionPayload
			if err := json.Unmarshal(p.env.Payload, &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.SuggestedReply == nil || *got.SuggestedReply != suggestText || got.Revision != 1 {
				t.Fatalf("payload = %+v, want the set at revision 1", got)
			}
			seen[p.connID] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("pushes reached %v, want a and b", seen)
		}
	}
}

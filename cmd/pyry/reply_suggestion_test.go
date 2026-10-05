package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turncommit"
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
	deliverSuggestionMessage(s, convID, "what next?")
	e.HandleFor(ctx, convID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
	e.HandleFor(ctx, convID, successEnd)
}

// deliverSuggestionMessage models a queue write followed by its confirmation.
func deliverSuggestionMessage(s *replySuggestions, convID, text string) {
	s.mu.Lock()
	id := uint64(1)
	if c := s.convs[convID]; c != nil {
		id = c.writeID + 1
	}
	s.mu.Unlock()
	s.beginWrite(convID, id)
	s.noteDelivered(convID, msgqueue.QueuedMessage{ID: id, Text: text})
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
			deliverSuggestionMessage(s, testConvID, " \n")
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
			e.HandleFor(ctx, testConvID, successEnd)
		}, false},
		{"subagent text only", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			deliverSuggestionMessage(s, testConvID, "go")
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "child", ParentToolCallID: "toolu_1"})
			e.HandleFor(ctx, testConvID, successEnd)
		}, false},
		{"error outcome", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			deliverSuggestionMessage(s, testConvID, "go")
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
			e.HandleFor(ctx, testConvID, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn, Outcome: "error_max_turns"})
		}, false},
		{"is_error", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			deliverSuggestionMessage(s, testConvID, "go")
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
			e.HandleFor(ctx, testConvID, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn, Outcome: "success", IsError: true})
		}, false},
		{"cancelled", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			deliverSuggestionMessage(s, testConvID, "go")
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
			e.HandleFor(ctx, testConvID, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonCancelled, Outcome: "success"})
		}, false},
		{"invalidated mid-turn", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			deliverSuggestionMessage(s, testConvID, "go")
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
			s.invalidate(testConvID)
			e.HandleFor(ctx, testConvID, successEnd)
		}, false},
		{"accept racing the drain's turn start", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			deliverSuggestionMessage(s, testConvID, "go")
			s.invalidate(testConvID) // a second message accepted before the drain saw the turn open
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
			e.HandleFor(ctx, testConvID, successEnd)
		}, false},
		{"send-now joins the turn", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			deliverSuggestionMessage(s, testConvID, "go")
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
			s.invalidate(testConvID)
			s.noteDelivered(testConvID, msgqueue.QueuedMessage{ID: 2, Text: "also this", SentNow: true})
			e.HandleFor(ctx, testConvID, successEnd)
		}, false},
		{"delivery confirmed after the turn end", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			s.beginWrite(testConvID, 1)
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
			e.HandleFor(ctx, testConvID, successEnd)
			s.noteDelivered(testConvID, msgqueue.QueuedMessage{ID: 1, Text: "go"})
		}, true},
		{"own accept, delivery confirmed after the turn end", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			suggestedTurn(e, s, testConvID)
			s.accepted(testConvID, 2)
			s.beginWrite(testConvID, 2)
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m2", Text: "Done again."})
			e.HandleFor(ctx, testConvID, successEnd)
			s.noteDelivered(testConvID, msgqueue.QueuedMessage{ID: 2, Text: "and then?"})
		}, true},
		{"next queued delivery re-arms", func(e *interactiveTurnEmitterV2, s *replySuggestions) {
			e.HandleFor(ctx, testConvID, turnevent.ThoughtChunk{Text: "earlier turn"})
			e.HandleFor(ctx, testConvID, successEnd)
			s.invalidate(testConvID)
			suggestedTurn(e, s, testConvID)
		}, true},
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

type stubResetter struct{ err error }

func (r stubResetter) StartNewSession(string) error { return r.err }

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
		{"refused /clear", func(_ *interactiveTurnEmitterV2, s *replySuggestions) {
			_ = suggestionResetter{inner: stubResetter{err: errors.New("refused")}, s: s}.StartNewSession(testConvID)
		}, false},
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

// TestReplySuggestions_HeldUntilDeliveryConfirmed: msgqueue confirms delivery on
// its own goroutine, so the suggestion can reach the owner first. It waits for
// that turn's confirmation, and an invalidation in between drops it.
func TestReplySuggestions_HeldUntilDeliveryConfirmed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, invalidateFirst := range []bool{false, true} {
		e, s := newSuggestionEmitter(testConvID)
		s.beginWrite(testConvID, 1)
		e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
		e.HandleFor(ctx, testConvID, successEnd)
		e.HandleFor(ctx, testConvID, turnevent.PromptSuggestion{Text: suggestText})
		if got := s.current(); len(got) != 0 {
			t.Fatalf("published %+v before the delivery was confirmed", got)
		}
		if invalidateFirst {
			s.invalidate(testConvID)
		}
		s.noteDelivered(testConvID, msgqueue.QueuedMessage{ID: 1, Text: "go", SentNow: invalidateFirst})
		p, ok := suggestionFor(t, s, testConvID)
		if published := ok && p.SuggestedReply != nil; published == invalidateFirst {
			t.Errorf("invalidated=%v: published = %v (%+v)", invalidateFirst, published, p)
		}
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

// Confirmations must not undo an accept that overtook the producing message.
func TestReplySuggestions_OvertakenTurn(t *testing.T) {
	t.Parallel()
	for _, firstConfirmed := range []bool{false, true} {
		t.Run(fmt.Sprint(firstConfirmed), func(t *testing.T) {
			e, s := newSuggestionEmitter(testConvID)
			ctx := context.Background()
			accept := func(id uint64) {
				suggestionEnqueuer{inner: stubEnqueuer{id: id}, s: s}.EnqueueSent(testConvID, "", "go", "go", nil, "", "", time.Time{})
			}
			accept(1)
			s.beginWrite(testConvID, 1)
			if firstConfirmed {
				s.noteDelivered(testConvID, msgqueue.QueuedMessage{ID: 1, Text: "first"})
			}
			accept(2)
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "m2", Text: "Done."})
			e.HandleFor(ctx, testConvID, successEnd)
			e.HandleFor(ctx, testConvID, turnevent.PromptSuggestion{Text: suggestText})
			id := uint64(1)
			if firstConfirmed {
				id = 2
				s.beginWrite(testConvID, 2)
			}
			s.noteDelivered(testConvID, msgqueue.QueuedMessage{ID: id, Text: "confirmed"})
			if p, ok := suggestionFor(t, s, testConvID); ok && p.SuggestedReply != nil {
				t.Fatal("a confirmation resurrected the overtaken turn's suggestion")
			}
		})
	}
}

func TestReplySuggestions_UnrelatedDelivery(t *testing.T) {
	t.Parallel()
	e, s := newSuggestionEmitter(testConvID)
	ctx := context.Background()
	e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "autonomous", Text: "Done."})
	e.HandleFor(ctx, testConvID, successEnd)
	e.HandleFor(ctx, testConvID, turnevent.PromptSuggestion{Text: suggestText})
	s.noteDelivered(testConvID, msgqueue.QueuedMessage{ID: 1, Text: "a different turn"})
	if p, ok := suggestionFor(t, s, testConvID); ok && p.SuggestedReply != nil {
		t.Fatal("unrelated delivery credited an autonomous turn")
	}
}

func TestReplySuggestions_DeliveryGate(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "failed", "dropped"} {
		t.Run(mode, func(t *testing.T) {
			e, s := newSuggestionEmitter(testConvID)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, release, delivered := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
			attempted := make(chan struct{})
			defer close(release)
			// The queue's real context carries the id and safe text. Stream content
			// arrives before the seam returns, so OnDelivered necessarily arrives late.
			q, err := msgqueue.New(msgqueue.Config{
				Deliver: s.trackDelivery(func(ctx context.Context, conv string, _ []byte) error {
					close(entered)
					defer close(attempted)
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-release:
					}
					if !turncommit.From(ctx)() {
						return turncommit.ErrDropped
					}
					// Model the enqueue adapter returning after the child already opened.
					s.accepted(conv, 1)
					e.HandleFor(ctx, conv, turnevent.TextChunk{MessageID: "m1", Text: "Done."})
					e.HandleFor(ctx, conv, successEnd)
					e.HandleFor(ctx, conv, turnevent.PromptSuggestion{Text: suggestText})
					if mode == "failed" {
						return errors.New("write failed")
					}
					return nil
				}),
				OnDelivered: func(conv string, msg msgqueue.QueuedMessage) {
					s.noteDelivered(conv, msg)
					close(delivered)
				},
				RetryInterval: time.Hour, Logger: discardLogger(),
			})
			if err != nil {
				t.Fatal(err)
			}
			id := q.EnqueueSent(testConvID, "", "go", "go", nil, "", "", time.Time{})
			done := make(chan error, 1)
			go func() { done <- q.Run(ctx) }()
			finished := false
			defer func() {
				cancel()
				if !finished {
					<-done
				}
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("delivery not entered")
			}
			// Waiting for idle must not establish a turn or destroy earlier state.
			if got := s.current(); len(got) != 0 {
				t.Fatal("waiting delivery published state")
			}
			if mode == "dropped" && !q.Remove(testConvID, id) {
				t.Fatal("remove refused before the gate")
			}
			release <- struct{}{}
			if mode == "success" {
				select {
				case <-delivered:
				case <-time.After(time.Second):
					t.Fatal("no delivery confirmation")
				}
				p, ok := suggestionFor(t, s, testConvID)
				if !ok || p.SuggestedReply == nil {
					t.Fatal("late own confirmation did not publish")
				}
			} else {
				// Queue cancellation joins the attempt and its failure invalidation.
				select {
				case <-attempted:
				case <-time.After(time.Second):
					t.Fatal("attempt did not finish")
				}
				cancel()
				<-done
				finished = true
				if p, ok := suggestionFor(t, s, testConvID); ok && p.SuggestedReply != nil {
					t.Fatal("unconfirmed write published")
				}
			}
		})
	}
}

func TestReplySuggestions_PruneUnpublished(t *testing.T) {
	t.Parallel()
	_, s := newSuggestionEmitter()
	s.accepted(testConvID, 1)
	s.current()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.convs) != 0 {
		t.Fatal("deleted unpublished conversation retained")
	}
}

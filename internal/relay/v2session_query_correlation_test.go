package relay

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

type testQueryCorrelationEvent struct {
	Sequence         int            `json:"sequence"`
	Stage            string         `json:"stage"`
	Peer             string         `json:"peer"`
	Inbox            string         `json:"inbox_generation"`
	Conversation     string         `json:"conversation"`
	Projection       string         `json:"projection"`
	Query            uint64         `json:"query_id"`
	Requested        uint64         `json:"requested_checkpoint"`
	Selected         *uint64        `json:"selected_checkpoint"`
	Completed        uint64         `json:"completed_checkpoint"`
	QueryCurrent     bool           `json:"query_is_current"`
	QueryContextDone bool           `json:"query_context_done"`
	InboxRetired     bool           `json:"inbox_retired"`
	Facts            map[string]any `json:"facts,omitempty"`
}

func TestProducerQueryRefusalCorrelation(t *testing.T) {
	var events []testQueryCorrelationEvent
	// Registered before the manager cleanup, so the trace is written after Run joins.
	t.Cleanup(func() {
		dir := os.Getenv("PYRY_QUERY_CORRELATION_DIR")
		if dir == "" {
			return
		}
		file, err := os.CreateTemp(dir, "query-correlation-*.json")
		if err != nil {
			t.Error("create correlation evidence:", err)
			return
		}
		defer func() {
			if err := file.Close(); err != nil {
				t.Error("close correlation evidence:", err)
			}
		}()
		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(events); err != nil {
			t.Error("write correlation evidence:", err)
		}
	})
	entered, providerDone := make(chan struct{}), make(chan struct{})
	queries := make(chan *threadQuery, 3)
	var calls atomic.Int32
	var p *testHostedPeer
	usable := func(version, from uint64) thread.QueryResult {
		content, revision := json.RawMessage(`{"text":"fixture"}`), uint64(2)
		if version > 2 {
			content, revision = json.RawMessage(`{"text":"fixturenew"}`), 3
		}
		return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: version, FromVersion: from,
			LowerOrder: 1, UpperOrder: 2, Continuation: 1,
			Items: []thread.Item{{ID: 1, Kind: "assistant_message", Order: 1, Rev: revision, Content: content}},
		}
	}
	p = testCatchUpPeer(t, func(ctx context.Context, request protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
		n := calls.Add(1)
		if n > 3 {
			return thread.QueryResult{}, errors.New("unexpected fixture query")
		}
		p.mgr.hostedMu.Lock()
		inbox := p.mgr.hostedInboxes[p.conn]
		var q *threadQuery
		if inbox != nil {
			q = inbox.query
		}
		p.mgr.hostedMu.Unlock()
		queries <- q
		if request.ConversationID != catchUpConversation || request.Epoch != "epoch" || n == 1 && request.Version != 0 || n > 1 && request.Version != 2 {
			t.Errorf("provider scope: call=%d version=%d epoch=%t conversation=%t", n, request.Version, request.Epoch == "epoch", request.ConversationID == catchUpConversation)
			return thread.QueryResult{}, errors.New("fixture query scope changed")
		}
		if n == 1 {
			return usable(2, 0), nil
		}
		if n == 2 {
			close(entered)
			defer close(providerDone)
			<-ctx.Done()
		}
		// The cancelled read still has a usable, error-free provider result. Its
		// refusal must come from cancellation, before encoding or reply selection.
		return usable(240, 2), nil
	}, nil)
	receive := func(id, version uint64, from *uint64) {
		t.Helper()
		for index := range 2 {
			env, _ := p.next()
			var items protocol.ThreadItemsPayload
			if env.Type != protocol.TypeThreadItems || env.InReplyTo == nil || *env.InReplyTo != id || json.Unmarshal(env.Payload, &items) != nil || items.ConversationID != catchUpConversation || items.Projection != "" || items.Epoch != "epoch" || items.Version != version {
				var failure protocol.ErrorPayload
				if env.Type == protocol.TypeError {
					if err := json.Unmarshal(env.Payload, &failure); err != nil {
						t.Fatal("decode fixture refusal:", err)
					}
				}
				t.Fatalf("fixture reply: type=%s correlated=%t version=%d conversation=%t epoch=%t projection=%q error=%s", env.Type, env.InReplyTo != nil && *env.InReplyTo == id, items.Version, items.ConversationID == catchUpConversation, items.Epoch == "epoch", items.Projection, failure.Code)
			}
			if index == 0 {
				if items.Final || len(items.Items) != 1 {
					t.Fatal("missing data before final marker")
				}
				var content struct{ Text string }
				expected, revision := "fixture", uint64(2)
				if version > 2 {
					expected, revision = "fixturenew", 3
				}
				if items.Items[0].ID != 1 || items.Items[0].Rev != revision || json.Unmarshal(items.Items[0].Content, &content) != nil || content.Text != expected {
					t.Fatal("provider snapshot did not retain the expected live state")
				}
			} else {
				if !items.Final || len(items.Items) != 0 {
					t.Fatal("missing independent final marker")
				}
				if from != nil && (items.FromVersion == nil || *items.FromVersion != *from) {
					t.Fatal("completed checkpoint changed")
				}
			}
		}
	}
	testCatchUpRequest(p, 2, 0)
	receive(2, 2, nil)
	first := <-queries
	if first == nil || first.inbox == nil {
		t.Fatal("baseline query was not admitted")
	}
	generation := first.inbox
	record := func(stage string, q *threadQuery, facts map[string]any) testQueryCorrelationEvent {
		t.Helper()
		p.mgr.hostedMu.Lock()
		current := p.mgr.hostedInboxes[p.conn]
		if q == nil || current != generation || q.inbox != generation || q.conversation != catchUpConversation || q.projection != "" {
			p.mgr.hostedMu.Unlock()
			t.Fatal("query peer, inbox generation, conversation or projection changed")
		}
		event := testQueryCorrelationEvent{Sequence: len(events) + 1, Stage: stage, Peer: "producer-1", Inbox: "inbox-1", Conversation: "conversation-1", Projection: "complete", Query: q.id, Requested: q.request.Version,
			Completed: generation.threadJoins[catchUpConversation].version, QueryCurrent: generation.query == q, QueryContextDone: q.ctx.Err() != nil, InboxRetired: generation.retired, Facts: facts}
		if q.captured != nil {
			version := q.captured.version
			event.Selected = &version
		}
		alive := generation.ctx.Err() == nil
		p.mgr.hostedMu.Unlock()
		if !alive || event.InboxRetired {
			t.Fatal("refusal retired the owning generation")
		}
		events = append(events, event)
		return event
	}
	baseline := record("baseline_final_received", first, map[string]any{"wire_type": protocol.TypeThreadItems, "in_reply_to": uint64(2), "final": true})
	if baseline.Completed != 2 || baseline.Selected == nil || *baseline.Selected != 2 {
		t.Fatal("baseline did not certify checkpoint two")
	}
	testCatchUpRequest(p, 3, 2)
	testHostedAwait(t, entered)
	cancelled := <-queries
	admitted := record("query_admitted", cancelled, nil)
	if admitted.Query != 3 || !admitted.QueryCurrent || admitted.QueryContextDone || admitted.Selected != nil || admitted.Requested != 2 {
		t.Fatal("query was not held before selection")
	}
	testCatchUpLive(t, p, 237)
	// Push returned after producer enqueue. Observe both the live row and refusal
	// before admitting the retry; flush alone would not fence hosted handling.
	record("producer_enqueue_acknowledged", cancelled, map[string]any{"live_version": uint64(237)})
	testHostedAwait(t, providerDone)
	record("usable_provider_completed", cancelled, map[string]any{"provider_usable": true, "provider_version": uint64(240), "provider_error": false})
	var refusal, live bool
	for range 2 {
		env, _ := p.next()
		switch env.Type {
		case protocol.TypeError:
			var failure protocol.ErrorPayload
			var fields map[string]json.RawMessage
			if refusal || env.InReplyTo == nil || *env.InReplyTo != 3 || json.Unmarshal(env.Payload, &failure) != nil || json.Unmarshal(env.Payload, &fields) != nil || len(fields) != 3 || failure.Code != "thread.query_failed" || failure.Message != "thread query could not complete" || !failure.Retryable {
				t.Fatal("missing correlated content-free retryable refusal")
			}
			refusal = true
			record("terminal_refusal_received", cancelled, map[string]any{"wire_type": env.Type, "in_reply_to": *env.InReplyTo, "code": failure.Code, "retryable": failure.Retryable, "wire_fields": len(fields)})
		case protocol.TypeThreadTextAppend:
			var update protocol.ThreadTextAppendPayload
			if live || env.InReplyTo != nil || json.Unmarshal(env.Payload, &update) != nil || update.ConversationID != catchUpConversation || update.Epoch != "epoch" || update.Version != 237 {
				t.Fatal("missing scoped unsolicited live update")
			}
			live = true
			record("live_frame_received", cancelled, map[string]any{"wire_type": env.Type, "live_version": update.Version})
		default:
			t.Fatal("cancelled query emitted data, reset or a completion certificate")
		}
	}
	state := record("refusal_did_not_complete", cancelled, nil)
	if !refusal || !live || !state.QueryContextDone || state.QueryCurrent || state.Selected != nil || state.Completed != 2 {
		t.Fatal("refusal certified state or retained the query slot")
	}
	testCatchUpRequest(p, 4, 2)
	checkpoint := uint64(2)
	receive(4, 240, &checkpoint)
	fresh := <-queries
	if fresh == cancelled {
		t.Fatal("fresh read reused the cancelled query token")
	}
	completed := record("fresh_final_received", fresh, map[string]any{"wire_type": protocol.TypeThreadItems, "in_reply_to": uint64(4), "final": true})
	if completed.Query != 4 || completed.Requested != 2 || completed.Completed != 240 || completed.Selected == nil || *completed.Selected != 240 || calls.Load() != 3 {
		t.Fatal("fresh read did not complete from the retained checkpoint")
	}
}

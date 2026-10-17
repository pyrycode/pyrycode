package main

import (
	"context"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// Request ownership is independent of the shared replay worker's lifetime. quietMu
// serializes acquisition with idle retirement; no request cancellation kills a
// worker another request needs. Removal and shutdown cancel all owners and join them.
type threadQueryDemand struct {
	context context.Context
	cancel  context.CancelFunc
	count   int
	done    chan struct{}
}

// Relay admission has already authenticated/authorized the request. The daemon
// owns loading, readiness and retirement; the bounded store APIs remain read-only.
func (s *threadShadow) queryThread(ctx context.Context, id conversations.ConversationID, store *thread.Store,
	query func(context.Context) (thread.QueryResult, error)) (thread.QueryResult, error) {
	return queryDaemonThread(ctx, s, id, store, query)
}

// All bounded reads retain the same demand through capture and rendering.
func queryDaemonThread[T any](ctx context.Context, s *threadShadow, id conversations.ConversationID, store *thread.Store,
	query func(context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.mu.Lock()
	p := s.publisher
	if p != nil {
		p.mu.Lock()
		if p.stopped || p.ctx.Err() != nil {
			p.mu.Unlock()
			s.mu.Unlock()
			return zero, context.Canceled
		}
		p.wg.Add(1)
		p.mu.Unlock()
		defer p.wg.Done()
	}
	s.mu.Unlock()
	if p != nil {
		stop := context.AfterFunc(p.ctx, cancel)
		defer stop()
		if p.ctx.Err() != nil {
			return zero, context.Canceled
		}
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	for {
		s.quietMu.Lock()
		retired := s.queryRetiring[id]
		if retired == nil {
			break
		}
		s.quietMu.Unlock()
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-retired:
		}
	}
	s.mu.Lock()
	_, registered := s.registry.Get(id)
	if !registered || s.removed[id] || s.queryStopping || ctx.Err() != nil {
		s.mu.Unlock()
		s.quietMu.Unlock()
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return zero, thread.ErrUnavailable
	}
	d := s.queryDemands[id]
	if d == nil {
		workerCtx, stop := context.WithCancel(s.ctx)
		d = &threadQueryDemand{context: workerCtx, cancel: stop, done: make(chan struct{})}
		if s.queryDemands == nil {
			s.queryDemands = make(map[conversations.ConversationID]*threadQueryDemand)
		}
		s.queryDemands[id] = d
	}
	d.count++
	s.queryDone.Add(1)
	// Queries share the conversation coordinator even before discovery has
	// enrolled a loaded or empty conversation.
	if s.workers[id] == nil {
		workerCtx, stop := context.WithCancel(s.ctx)
		w := &shadowConversation{cancel: stop, done: make(chan struct{})}
		s.workers[id] = w
		go s.follow(workerCtx, id, w)
	}
	w := s.workers[id]
	s.mu.Unlock()
	err := store.Load(s.ctx, id)
	s.quietMu.Unlock()
	stop := context.AfterFunc(d.context, cancel)
	defer func() {
		stop()
		s.quietMu.Lock()
		d.count--
		if d.count == 0 {
			delete(s.queryDemands, id)
			d.cancel()
			close(d.done)
		}
		s.quietMu.Unlock()
		s.queryDone.Done()
	}()
	if err != nil {
		return zero, err
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		switch store.Status(id).State {
		case thread.StateUsable:
			// Establish the live boundary before a query can certify its capture.
			// Later commits must be delivered from it or force reconciliation.
			if !s.publishThread(ctx, id, w, true) {
				if err := ctx.Err(); err != nil {
					return zero, err
				}
				return zero, thread.ErrUnavailable
			}
			result, err := query(ctx)
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
			return result, err
		case thread.StateRebuilding:
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-tick.C:
			}
		default:
			return zero, thread.ErrUnavailable
		}
	}
}

func (s *threadShadow) cancelThreadDemand(id conversations.ConversationID) {
	s.quietMu.Lock()
	d := s.queryDemands[id]
	if d != nil {
		d.cancel()
	}
	s.quietMu.Unlock()
	if d != nil {
		<-d.done
	}
}

func (s *threadShadow) stopThreadDemands() {
	s.quietMu.Lock()
	s.queryStopping = true
	for _, d := range s.queryDemands {
		d.cancel()
	}
	s.quietMu.Unlock()
	s.queryDone.Wait()
}

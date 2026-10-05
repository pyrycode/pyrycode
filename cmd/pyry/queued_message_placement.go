package main

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"sync"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
)

func (p *sendNowPlacement) bindQueued(ctx context.Context, sink *streamTurnSink, store *history.Store, isClaude func(string) bool, logger *slog.Logger) {
	p.record = newOperatorMessageHistory(store, sink.publishOperator, nil, logger)
	p.dispatch = func(commit func()) { sink.dispatchPlacement(ctx, commit) }
	p.isClaude = isClaude
	idle := p.idle
	sink.placementIdle.Store(&idle)
}

func (s *streamTurnSink) setOperatorPublisher(fn func(operatorMessage)) {
	s.operatorPublisher.Store(&fn)
}

func (s *streamTurnSink) publishOperator(m operatorMessage) {
	if fn := s.operatorPublisher.Load(); fn != nil {
		(*fn)(m)
	}
}

func (s *streamTurnSink) observePlacementIdle(sessionID string) {
	if fn := s.placementIdle.Load(); fn != nil {
		(*fn)(sessionID)
	}
}

func (s *streamTurnSink) dispatchPlacement(ctx context.Context, commit func()) {
	select {
	case <-ctx.Done():
	case s.placementCommands <- commit:
	}
}

type placementWriteLock struct {
	sync.Mutex
	users int // guarded by placement.mu
}

// lockWrite keeps registration order equal to write order, including competing
// ordinary and send-now deliveries. Unused conversation locks are retired.
func (p *sendNowPlacement) lockWrite(convID string) func() {
	p.mu.Lock()
	if p.writes == nil {
		p.writes = make(map[string]*placementWriteLock)
	}
	w := p.writes[convID]
	if w == nil {
		w = &placementWriteLock{}
		p.writes[convID] = w
	}
	w.users++
	p.mu.Unlock()
	w.Lock()
	return func() {
		w.Unlock()
		p.mu.Lock()
		w.users--
		if w.users == 0 {
			delete(p.writes, convID)
		}
		p.mu.Unlock()
	}
}

// write registers the safe queued projection at the final composed write
// boundary. The result signal precedes caller cleanup: an echo may be waiting
// for it while the drain holds the post-publication gate.
func (p *sendNowPlacement) write(ctx context.Context, convID string, payload []byte, write func() error) error {
	msg, ok := msgqueue.DeliveryMessage(ctx)
	if !ok || p.record == nil {
		return write()
	}
	unlock := p.lockWrite(convID)
	defer unlock()
	e := &placedMessage{id: msg.ID, digest: sha256.Sum256(payload), queued: true,
		sentNow: msg.SentNow, outcome: make(chan struct{}), commit: func() { p.record(convID, msg) }}
	p.mu.Lock()
	if p.managed == nil {
		p.managed = make(map[string]map[uint64]*placedMessage)
	}
	if p.managed[convID] == nil {
		p.managed[convID] = make(map[uint64]*placedMessage)
	}
	p.managed[convID][msg.ID] = e
	p.pending[convID] = append(p.pending[convID], e)
	p.mu.Unlock()
	e.writeErr = write()
	close(e.outcome)
	if e.writeErr != nil {
		p.mu.Lock()
		p.removeLocked(convID, e)
		p.forgetLocked(convID, msg.ID)
		p.mu.Unlock()
		return e.writeErr
	}
	go func() {
		if p.waitIdle(p.ctx, convID) != nil {
			return
		}
		p.dispatch(func() { p.takeQueued(convID, e) })
	}()
	return nil
}

func (p *sendNowPlacement) forgetLocked(convID string, id uint64) {
	delete(p.managed[convID], id)
	if len(p.managed[convID]) == 0 {
		delete(p.managed, convID)
	}
}

func (p *sendNowPlacement) takeQueued(convID string, e *placedMessage) {
	select {
	case <-p.ctx.Done():
		return
	case <-e.outcome:
		if e.writeErr == nil {
			p.take(convID, e)
		}
	}
}

// idle runs on the drain before a closing event releases ordinary delivery's
// busy mark. No-echo entries are therefore committed by the answering turn's
// idle boundary even when OnDelivered is withheld. Send-now keeps its grace.
func (p *sendNowPlacement) idle(sessionID string) {
	convID, ok := p.resolve(sessionID)
	if !ok {
		return
	}
	p.mu.Lock()
	entries := append([]*placedMessage(nil), p.pending[convID]...)
	p.mu.Unlock()
	for _, e := range entries {
		if e.queued && !e.sentNow {
			p.takeQueued(convID, e)
		}
	}
}

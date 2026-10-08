package main

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"sync"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

func (p *sendNowPlacement) bindQueued(ctx context.Context, sink *streamTurnSink, store *history.Store, isClaude func(string) bool, logger *slog.Logger) {
	p.confirmationDispatch = func(commit func()) {
		if sink.runtimeEnabled.Load() {
			sink.dispatchPlacement(ctx, commit)
		} else {
			commit()
		}
	}
	p.record = func(convID string, msg msgqueue.QueuedMessage, source ...history.SessionProvenance) {
		newOperatorMessageHistory(store, sink.publishOperator, nil, logger, source...)(convID, msg)
	}
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
	if s.runtimeEnabled.Load() {
		if ctx.Err() == nil {
			s.queueBoundary(sessions.SessionTransition{}, commit, nil)
		}
		return
	}
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
func (p *sendNowPlacement) write(ctx context.Context, convID string, payload []byte, write func() error, source ...*history.SessionProvenance) error {
	msg, ok := msgqueue.DeliveryMessage(ctx)
	if !ok || p.record == nil {
		return write()
	}
	unlock := p.lockWrite(convID)
	defer unlock()
	e := &placedMessage{id: msg.ID, digest: sha256.Sum256(payload), queued: true,
		sentNow: msg.SentNow, outcome: make(chan struct{})}
	e.commit = func() { p.record(convID, msg, e.source) }
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
	if e.writeErr == nil && len(source) != 0 && source[0] != nil {
		e.source = *source[0]
	}
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

type operatorProvenanceWriter interface {
	operatorProvenance() history.SessionProvenance
}

// writeOperatorTurn captures at the final writer call, after activation and
// idle waits. Only successful writes retain source information for recording.
func writeOperatorTurn(ctx context.Context, convID string, payload []byte, w handlers.TurnWriter, p *sendNowPlacement, sendNow bool) error {
	provider, known := w.(operatorProvenanceWriter)
	claude := sendNow
	if known {
		claude = provider.operatorProvenance().Kind == "claude"
	} else if !sendNow && p != nil {
		claude = p.isClaude(convID)
	}
	var source history.SessionProvenance
	write := func() error {
		if known {
			source = provider.operatorProvenance()
		}
		return w.WriteUserTurn(ctx, convID, payload)
	}
	if p != nil && claude {
		return p.write(ctx, convID, payload, write, &source)
	}
	if err := write(); err != nil {
		return err
	}
	if msg, ok := msgqueue.DeliveryMessage(ctx); ok && p != nil && source.Kind != "" {
		p.mu.Lock()
		if p.sources == nil {
			p.sources = make(map[string]map[uint64]history.SessionProvenance)
		}
		if p.sources[convID] == nil {
			p.sources[convID] = make(map[uint64]history.SessionProvenance)
		}
		p.sources[convID][msg.ID] = source
		p.mu.Unlock()
	}
	return nil
}

// takeSource consumes confirmation-only attribution. Claude keeps its source
// on the managed placement entry, synchronized by the write outcome signal.
func (p *sendNowPlacement) takeSource(convID string, id uint64) history.SessionProvenance {
	if p == nil {
		return history.SessionProvenance{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	source := p.sources[convID][id]
	delete(p.sources[convID], id)
	if len(p.sources[convID]) == 0 {
		delete(p.sources, convID)
	}
	return source
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

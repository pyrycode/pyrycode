package main

import (
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// daemonInventoryIngress binds an inventory once, before caches or downstream
// decorators can delay it. Both holds use the same capture and delivery path.
type daemonInventoryIngress struct {
	sink *streamTurnSink
	tag  *streamSessionTag
}

func daemonInventoryEvent(ev turnevent.Event) bool {
	switch ev.(type) {
	case turnevent.ModelList, turnevent.SlashCommandList:
		return true
	}
	return false
}

// prepare stores the payload/source pair and admits its family under the
// transition boundary. store only updates an in-memory hold. Persistence and
// forwarding run after the boundary, using this immutable envelope.
func (i *daemonInventoryIngress) prepare(ev turnevent.Event, store func(daemonLiveSource)) *streamTurnEnvelope {
	s := i.sink
	if s.live == nil {
		store(daemonLiveSource{})
		return nil
	}
	s.offerMu.Lock()
	defer s.offerMu.Unlock()
	id := i.tag.ID()
	env := streamTurnEnvelope{sessionID: id, incarnation: i.tag.incarnation.Load(), ev: ev,
		source: history.SessionProvenance{Kind: "claude", SessionID: id}}
	i.tag.lastSource.Store(&env.source)
	src := s.live.capture(id, env.incarnation, env.source, false)
	store(src)
	env.live = s.live.acceptEvent(src, ev)
	return &env
}

// downstream leaves ordinary events on the legacy path. Inventory envelopes
// have already been captured and are offered by their hold after decorators run.
func (i *daemonInventoryIngress) downstream(next func(turnevent.Event)) func(turnevent.Event) {
	return func(ev turnevent.Event) {
		if i.sink.live == nil || !daemonInventoryEvent(ev) {
			next(ev)
		}
	}
}

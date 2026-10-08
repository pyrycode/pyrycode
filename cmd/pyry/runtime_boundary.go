package main

import (
	"context"
	"slices"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type runtimeBoundary struct {
	exitAfter uint64
	fact      sessions.SessionTransition
	after     uint64
	publish   func()
	done      chan struct{}
}

// queueBoundary uses the same short lock as output acceptance. The notification
// is retained independently of wake capacity; ordinary pool callbacks never
// wait for history, fanout, the publication gate or the drain.
func (s *streamTurnSink) queueBoundary(t sessions.SessionTransition, publish func(), done chan struct{}) {
	s.offerMu.Lock()
	after := s.queued
	if t.PreviousID != "" {
		after = s.runtimeLastQueued[string(t.PreviousID)]
	}
	if t.OccurredAt.IsZero() {
		t.OccurredAt = time.Now().UTC()
	}
	if s.runtimeHolds == nil {
		s.runtimeHolds = make(map[string]int)
	}
	s.runtimeHolds[t.ConversationID]++
	s.runtimePending = append(s.runtimePending, runtimeBoundary{fact: t, after: after, publish: publish, done: done, exitAfter: s.exits.Load()})
	s.offerMu.Unlock()
	select {
	case s.runtimeWake <- struct{}{}:
	default:
	}
}

func (s *streamTurnSink) boundaryPending(convID string) bool {
	s.offerMu.Lock()
	defer s.offerMu.Unlock()
	return s.runtimeHolds[convID] > 0
}

// takeBoundaries keeps both A→B and B→C even when consumers are delayed. A
// captured occurrence orders ready facts; the output watermark prevents a
// boundary from passing text already accepted from its predecessor.
func (s *streamTurnSink) takeBoundaries(processed uint64) []runtimeBoundary {
	s.offerMu.Lock()
	defer s.offerMu.Unlock()
	var ready, pending []runtimeBoundary
	for start := 0; start < len(s.runtimePending); {
		end := start
		for end < len(s.runtimePending) && s.runtimePending[end].fact.ConversationID != "" {
			end++
		}
		slices.SortStableFunc(s.runtimePending[start:end], func(a, b runtimeBoundary) int { return a.fact.OccurredAt.Compare(b.fact.OccurredAt) })
		start = end + 1
	}
	blocked := make(map[string]bool)
	anyBlocked := false
	for _, b := range s.runtimePending {
		needsStop := b.fact.Cause == sessions.CauseIdleSleep || b.fact.Cause == sessions.CauseCapacityEviction || b.fact.Reason == sessions.ReasonEviction
		stopped := !needsStop || s.runtimeStopSeen[string(b.fact.PreviousID)] > b.exitAfter
		if needsStop {
			b.after = max(b.after, s.runtimeLastQueued[string(b.fact.PreviousID)])
		}
		if b.after <= processed && stopped && !blocked[b.fact.ConversationID] && !(b.fact.ConversationID == "" && anyBlocked) {
			ready = append(ready, b)
		} else {
			blocked[b.fact.ConversationID] = true
			anyBlocked = true
			pending = append(pending, b)
		}
	}
	s.runtimePending = pending
	return ready
}

// installRuntimeHistory runs before pool and delivery workers start. It leaves
// older emitter constructors usable without turning their fixtures into new
// thread facts. Both daemon relay configurations install this same path.
func installRuntimeHistory(s *streamTurnSink, e *interactiveTurnEmitterV2, busy *turnBusyTracker) {
	e.runtimeFacts = true
	s.runtimeEnabled.Store(true)
	if busy != nil {
		busy.runtimeSink = s
	}
}

func (s *streamTurnSink) publishRuntimeBoundaries(ctx context.Context, e *interactiveTurnEmitterV2, busy *turnBusyTracker, processed uint64) {
	for _, b := range s.takeBoundaries(processed) {
		unlock := busy.lockPostBoundary()
		t := b.fact
		_, divider := runtimeDivider(t)
		if divider || (t.PreviousID != "" && (t.Reason == sessions.ReasonClear || t.Reason == sessions.ReasonEviction)) {
			cause := string(t.Cause)
			if cause == "" {
				cause = "unknown"
			}
			e.closeRuntimeSource(ctx, t.ConversationID, string(t.PreviousID), cause, t.OccurredAt, true, 0)
		}
		b.publish()
		if t.ConversationID != "" && !e.hasRuntimeTurn(t.ConversationID) {
			sid := string(t.NewID)
			if sid == "" {
				sid = string(t.PreviousID)
			}
			s.observePlacementIdle(sid)
			if busy != nil {
				busy.setBusy(t.ConversationID, false, toolCallDelta{})
			}
			busy.publishPostBoundary(t.ConversationID, false)
		}
		s.offerMu.Lock()
		s.runtimeHolds[t.ConversationID]--
		if s.runtimeHolds[t.ConversationID] == 0 {
			delete(s.runtimeHolds, t.ConversationID)
		}
		s.offerMu.Unlock()
		unlock()
		if b.done != nil {
			close(b.done)
		}
	}
}

func runtimeExitTime() time.Time { return time.Now().UTC() }

// Runtime producer callbacks retain the actual last output source independently
// of the routing tag, which RestartFresh rotates before the old child's exit.
func (s *streamTurnSink) sinkForSessionTag(tag *streamSessionTag, kind string) func(turnevent.Event) {
	return s.sinkForTag(func() string {
		id := tag.ID()
		source := history.SessionProvenance{Kind: kind, SessionID: id}
		tag.lastSource.Store(&source)
		return id
	}, kind)
}

func (s *streamTurnSink) exitForSessionTag(tag *streamSessionTag) func() {
	return func() {
		env := streamTurnEnvelope{sessionID: tag.ID(), exit: true, exitEpoch: s.exits.Add(1), occurredAt: runtimeExitTime()}
		if source := tag.lastSource.Load(); source != nil {
			env.source = *source
		}
		if retired := tag.retiringSource.Swap(nil); retired != nil {
			env.source.SessionID = *retired
		}
		if !s.offer(env, true) {
			s.logger.Warn("relay: stream-turn exit retained; sink full", "event", "stream_turn.exit_sink_full", "session_id", env.sessionID)
		}
	}
}

func (s *streamTurnSink) noteRuntimeStop(env streamTurnEnvelope) bool {
	s.offerMu.Lock()
	defer s.offerMu.Unlock()
	if s.runtimeStopSeen == nil {
		s.runtimeStopSeen = make(map[string]uint64)
	}
	id := env.source.SessionID
	if id == "" {
		id = env.sessionID
	}
	s.runtimeStopSeen[id] = max(s.runtimeStopSeen[id], env.exitEpoch)
	for _, b := range s.runtimePending {
		if (string(b.fact.PreviousID) == id || (env.source.SessionID == "" && string(b.fact.NewID) == env.sessionID)) && b.fact.ConversationID != "" {
			return true
		}
	}
	return false
}

package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// refreshThreadSummary announces the end of a temporary retirement gap. The
// delivery projection resolves authority afresh; this notification certifies no
// watermark itself, including when an append has invalidated the retired view.
func (s *threadShadow) refreshThreadSummary(ctx context.Context, id conversations.ConversationID) {
	s.mu.Lock()
	p := s.publisher
	if p != nil {
		p.mu.Lock()
		if p.stopped {
			p = nil
		} else {
			p.wg.Add(1)
		}
		s.publisher.mu.Unlock()
	}
	s.mu.Unlock()
	if p == nil {
		return
	}
	defer p.wg.Done()
	operation, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	defer cancel()
	row, ok := s.registry.Get(id)
	if !ok || operation.Err() != nil {
		return
	}
	var label *string
	if value, present := s.registry.WorkspaceLabel(row.Cwd); present {
		label = &value
	}
	payload, err := json.Marshal(protocol.ConversationUpdatedPayload{
		ID: string(row.ID), Name: row.Name, Cwd: row.Cwd, WorkspaceLabel: label,
		IsPromoted: row.IsPromoted, IsArchived: row.IsArchived, IsMuted: row.IsMuted,
		PinnedAt: row.PinnedAt, ReadUpTo: row.ReadUpTo, LastUsedAt: row.LastUsedAt,
	})
	if err != nil {
		p.log.Warn("relay: thread summary encoding failed", "event", "daemon.thread.summary_encode_err")
		return
	}
	env := protocol.Envelope{ID: p.nextID.Add(1), Type: protocol.TypeConversationUpdated, TS: time.Now().UTC(), Payload: payload}
	for _, peer := range p.active(operation, string(id)) {
		if operation.Err() != nil {
			return
		}
		if !peer.Interactive || !peer.Thread {
			continue
		}
		if err := p.push(operation, peer.ConnID, env); err != nil && operation.Err() == nil {
			p.log.Debug("relay: thread summary push failed", "event", "daemon.thread.summary_push_err")
		}
	}
}

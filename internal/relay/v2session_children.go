package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// parseThreadChildCursor checks final raw values so null and absent zero-valued
// fields cannot become valid cursor positions, including duplicate-key nulls.
func parseThreadChildCursor(raw json.RawMessage, r protocol.ThreadExpansionRequest) (*protocol.ThreadExpansionCursor, bool) {
	var fields map[string]json.RawMessage
	var c protocol.ThreadExpansionCursor
	if json.Unmarshal(raw, &fields) != nil || fields == nil || json.Unmarshal(raw, &c) != nil {
		return nil, false
	}
	for _, name := range []string{"conversation_id", "epoch"} {
		var v *string
		if json.Unmarshal(fields[name], &v) != nil || v == nil {
			return nil, false
		}
	}
	for _, name := range []string{"item_id", "rev", "version", "order", "id"} {
		var v *uint64
		if json.Unmarshal(fields[name], &v) != nil || v == nil {
			return nil, false
		}
	}
	return &c, c.ID != 0 && c.Matches(r, c.Version)
}

func (m *V2SessionManager) runThreadChildren(ctx context.Context, s *V2Session, q *threadQuery) {
	defer func() {
		q.cancel()
		m.hostedMu.Lock()
		if q.inbox.query == q {
			q.inbox.query = nil
		}
		m.hostedMu.Unlock()
	}()
	fail := func() { _ = m.deliverThread(ctx, s.connID, threadQueryError(q.id), q) } // Best effort; retired generations cannot receive refusals.
	if q.ctx.Err() != nil {
		fail()
		return
	}
	r, err := m.cfg.ThreadChildren(q.ctx, *q.body)
	if err != nil || r.Err != nil || r.State != thread.StateUsable || string(r.ConversationID) != q.conversation || r.More == r.Complete || q.ctx.Err() != nil {
		fail()
		return
	}
	p := protocol.ThreadChildrenPayload{ConversationID: string(r.ConversationID), Epoch: r.Epoch, Version: r.Version, ItemID: r.Parent, Rev: r.ParentRev, Children: []protocol.ThreadSummary{}}
	for _, row := range r.Items {
		p.Children = append(p.Children, threadSummaryFrom(row))
	}
	frames, err := protocol.EncodeThreadChildren(protocol.Envelope{TS: time.Now().UTC(), InReplyTo: &q.id}, *q.body, p, r.More)
	if err != nil || q.ctx.Err() != nil {
		fail()
		return
	}
	for _, raw := range frames {
		var e protocol.Envelope
		if json.Unmarshal(raw, &e) != nil {
			fail()
			return
		}
		if err := m.deliverThread(q.ctx, s.connID, e, q); err != nil {
			fail()
			return
		}
	}
}

package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func (m *V2SessionManager) runThreadToolBody(ctx context.Context, s *V2Session, q *threadQuery) {
	defer func() {
		q.cancel()
		// A withheld refusal cannot leave a permanent unfinished-query slot.
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
	r, err := m.cfg.ThreadToolBody(q.ctx, *q.body)
	if err != nil || r.Err != nil || r.State != thread.StateUsable || q.ctx.Err() != nil {
		fail()
		return
	}
	env := protocol.Envelope{TS: time.Now().UTC(), InReplyTo: &q.id}
	frames, err := protocol.EncodeThreadToolBody(env, *q.body, protocol.ThreadToolBodyPayload{
		ConversationID: q.conversation, Epoch: r.Epoch, Version: r.Version,
		ItemID: r.ID, Rev: r.Rev, Kind: r.Kind, Content: r.Content,
	})
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

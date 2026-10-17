package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

const maxThreadJoins = 256

type threadQuery struct {
	id           uint64
	conversation string
	request      protocol.ThreadCatchUpPayload
	page         *protocol.ThreadPagePayload
	body         *protocol.ThreadExpansionRequest
	children     bool
	inbox        *hostedInbox
	ctx          context.Context
	cancel       context.CancelFunc
}
type threadJoin struct {
	epoch   string
	version uint64
}

func threadQueryError(id uint64) protocol.Envelope {
	payload, _ := json.Marshal(protocol.ErrorPayload{Code: "thread.query_failed", Message: "thread query could not complete", Retryable: true})
	return protocol.Envelope{Type: protocol.TypeError, TS: time.Now().UTC(), InReplyTo: &id, Payload: payload}
}

// admitThreadQuery runs after Noise authentication and before any provider work.
func (m *V2SessionManager) admitThreadQuery(ctx context.Context, s *V2Session, raw []byte) {
	var fields map[string]json.RawMessage
	var env protocol.Envelope
	var id *uint64
	if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(raw, &env) != nil || json.Unmarshal(fields["id"], &id) != nil || id == nil {
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
		return
	}
	fail := func() {
		e := threadQueryError(*id)
		if s.hosted == nil {
			if err := m.forwardEnvelope(ctx, s.connID, e); err != nil {
				m.closeWith(ctx, s, StatusProtocolMismatch, nil)
			}
			return
		}
		m.hostedEnqueue(ctx, s, &hostedOutput{env: &e})
	}
	var p struct {
		Conversation *string `json:"conversation_id"`
		Limit        *uint64 `json:"limit"`
	}
	var payload map[string]json.RawMessage
	page := env.Type == protocol.TypeThreadPage
	body := env.Type == protocol.TypeThreadReadToolBody
	children := env.Type == protocol.TypeThreadReadChildren
	expanding := body || children
	if json.Unmarshal(env.Payload, &p) != nil || json.Unmarshal(env.Payload, &payload) != nil || p.Conversation == nil {
		fail()
		return
	}
	var beforeOrder, version *uint64
	var epoch *string
	var expansion *protocol.ThreadExpansionRequest
	if expanding {
		var itemID, rev *uint64
		_, cursor := payload["cursor"]
		if body && cursor || json.Unmarshal(payload["epoch"], &epoch) != nil || epoch == nil || *epoch == "" ||
			json.Unmarshal(payload["item_id"], &itemID) != nil || itemID == nil || *itemID == 0 ||
			json.Unmarshal(payload["expected_rev"], &rev) != nil || rev == nil || *rev == 0 {
			fail()
			return
		}
		expansion = &protocol.ThreadExpansionRequest{ConversationID: *p.Conversation, Epoch: *epoch, ItemID: *itemID, ExpectedRev: *rev}
		if children && cursor {
			c, ok := parseThreadChildCursor(payload["cursor"], *expansion)
			if !ok {
				fail()
				return
			}
			expansion.Cursor = c
		}
	} else if page {
		if json.Unmarshal(payload["before_order"], &beforeOrder) != nil || beforeOrder == nil {
			fail()
			return
		}
	} else if json.Unmarshal(payload["epoch"], &epoch) != nil || epoch == nil || json.Unmarshal(payload["version"], &version) != nil || version == nil {
		fail()
		return
	}
	limit := 0
	if p.Limit != nil {
		if !children && *p.Limit > uint64(^uint(0)>>1) {
			fail()
			return
		}
		limit = int(min(*p.Limit, uint64(protocol.MaxThreadQueryLimit)))
	} else if _, present := payload["limit"]; present {
		fail()
		return
	}
	limit, err := protocol.NormalizeThreadQueryLimit(limit)
	providerMissing := body && m.cfg.ThreadToolBody == nil || children && m.cfg.ThreadChildren == nil || !expanding && (page && m.cfg.ThreadPage == nil || !page && m.cfg.ThreadCatchUp == nil)
	if err != nil || !conversations.ValidID(*p.Conversation) || s.state != V2StateOpen || m.liveWithheld(s, *p.Conversation) || !s.thread || !s.interactive || expanding && !s.threadExpansion || !m.hostedAuthorized(s) || providerMissing || m.hostedSender() == nil {
		fail()
		return
	}
	if s.hosted == nil {
		m.initHosted(ctx, s)
	}
	in := s.hosted.inbox
	m.hostedMu.Lock()
	_, joined := in.threadJoins[*p.Conversation]
	if in.query != nil || !expanding && !joined && len(in.threadJoins) >= maxThreadJoins {
		m.hostedMu.Unlock()
		fail()
		return
	}
	queryCtx, cancel := context.WithCancel(in.ctx)
	q := &threadQuery{id: *id, conversation: *p.Conversation, ctx: queryCtx, cancel: cancel, inbox: in, body: expansion, children: children}
	if expansion != nil {
		expansion.Limit = limit
	}
	if page {
		q.page = &protocol.ThreadPagePayload{ConversationID: *p.Conversation, BeforeOrder: *beforeOrder, Limit: limit}
	} else if !expanding {
		q.request = protocol.ThreadCatchUpPayload{ConversationID: *p.Conversation, Epoch: *epoch, Version: *version, Limit: limit}
	}
	in.query = q
	m.hostedMu.Unlock()
	m.enqueueAppFrame(ctx, s, appFrameJob{kind: appFrameThreadQuery, query: q})
}

func (m *V2SessionManager) runThreadQuery(ctx context.Context, s *V2Session, q *threadQuery) {
	if q.children {
		m.runThreadChildren(ctx, s, q)
		return
	}
	if q.body != nil {
		m.runThreadToolBody(ctx, s, q)
		return
	}
	defer q.cancel()
	fail := func() { _ = m.deliverThread(ctx, s.connID, threadQueryError(q.id), q) } // best effort terminal refusal; retirement cannot receive it.
	if q.ctx.Err() != nil {
		fail()
		return
	}
	var result thread.QueryResult
	var err error
	if q.page != nil {
		result, err = m.cfg.ThreadPage(q.ctx, *q.page)
	} else {
		result, err = m.cfg.ThreadCatchUp(q.ctx, q.request)
	}
	if err != nil || result.Err != nil || result.State != thread.StateUsable || q.ctx.Err() != nil || q.page != nil && result.ResetRequired {
		fail()
		return
	}
	env := protocol.Envelope{TS: time.Now().UTC(), InReplyTo: &q.id}
	var reply any
	if result.ResetRequired {
		reply = protocol.ThreadResetPayload{ConversationID: q.conversation, Epoch: result.Epoch, Version: result.Version}
	} else {
		p := protocol.ThreadItemsPayload{ConversationID: q.conversation, Epoch: result.Epoch, Version: result.Version, Final: true}
		if q.page == nil && q.request.Version > 0 {
			p.FromVersion = &result.FromVersion
		} else {
			p.LowerOrder = &result.LowerOrder
			p.UpperOrder = &result.UpperOrder
			p.Continuation = &result.Continuation
			p.OlderExists = &result.OlderExists
		}
		for _, i := range result.Items {
			if q.ctx.Err() != nil {
				fail()
				return
			}
			p.Items = append(p.Items, protocol.ThreadItem{ID: i.ID, Kind: i.Kind, Order: i.Order, Rev: i.Rev, EndedOrder: i.EndedOrder, Session: i.Session, Agent: i.Agent, NoChild: i.NoChild, Turn: i.Turn, Parent: i.Parent, Status: i.Status, Active: i.Active, Shown: i.Shown, Summary: i.Summary, Subtype: i.Subtype, Content: i.Content})
		}
		reply = p
	}
	if q.ctx.Err() != nil {
		fail()
		return
	}
	frames, err := protocol.EncodeThreadReply(env, reply)
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

// cancelThreadQuery fences live admission against final selection without holding
// pushMu. The original bounded push queue still owns all live buffering.
func (m *V2SessionManager) cancelThreadQuery(connID string, env protocol.Envelope) {
	if !threadItemUpdate(env.Type) || env.InReplyTo != nil {
		return
	}
	conversation := pushedConversationID(env)
	m.hostedMu.Lock()
	defer m.hostedMu.Unlock()
	if in := m.hostedInboxes[connID]; in != nil && in.query != nil && in.query.conversation == conversation {
		in.query.cancel()
	}
}

// threadQueryGate runs immediately before sealing a selected ordered-lane head.
// Its successful marker cut and producer cancellation share one mutex. A later
// transport failure retires the generation, so the cut cannot survive a lost marker.
func (m *V2SessionManager) threadQueryGate(s *V2Session, o *hostedOutput) error {
	if o.env == nil {
		return nil
	}
	env := *o.env
	m.hostedMu.Lock()
	defer m.hostedMu.Unlock()
	in := s.hosted.inbox
	if threadItemUpdate(env.Type) && env.InReplyTo == nil {
		var p struct {
			Conversation string `json:"conversation_id"`
			Epoch        string `json:"epoch"`
			Version      uint64 `json:"version"`
		}
		if json.Unmarshal(env.Payload, &p) != nil {
			return ErrThreadWithheld
		}
		if in.query != nil && in.query.conversation == p.Conversation {
			in.query.cancel()
		}
		if j, ok := in.threadJoins[p.Conversation]; ok && j.epoch == p.Epoch && p.Version <= j.version {
			return ErrThreadWithheld
		}
	}
	if o.query == nil || in.query != o.query {
		return nil
	}
	if env.Type == protocol.TypeError || env.Type == protocol.TypeThreadReset {
		in.query = nil
		return nil
	}
	if q := o.query; q.body != nil && (env.Type == protocol.TypeThreadToolBody || env.Type == protocol.TypeThreadChildren) {
		var body struct {
			Final bool `json:"final"`
		}
		if json.Unmarshal(env.Payload, &body) == nil && body.Final {
			if err := q.ctx.Err(); err != nil {
				return err
			}
			in.query = nil
		}
		return nil
	}
	var p protocol.ThreadItemsPayload
	if env.Type == protocol.TypeThreadItems && json.Unmarshal(env.Payload, &p) == nil && p.Final {
		if err := o.query.ctx.Err(); err != nil {
			return err
		}
		if in.threadJoins == nil {
			in.threadJoins = make(map[string]threadJoin)
		}
		in.threadJoins[o.query.conversation] = threadJoin{p.Epoch, p.Version}
		in.query = nil
	}
	return nil
}

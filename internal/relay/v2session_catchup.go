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
	projection   string // empty is complete; otherwise protocol.ThreadProjectionCompact
	inbox        *hostedInbox
	ctx          context.Context
	cancel       context.CancelFunc
	captured     *threadJoin // Selected complete-reply checkpoint, guarded by hostedMu.
}

// threadJoin is one conversation's completed checkpoint on one connection.
// Projection owns that conversation's view: non-initializing requests must match it.
type threadJoin struct {
	epoch      string
	version    uint64
	projection string
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
	if err != nil || !conversations.ValidID(*p.Conversation) || s.state != V2StateOpen || m.threadConversationWithheld(s, *p.Conversation) || expanding && (!s.threadExpansion || !s.interactive) || !m.hostedAuthorized(s) || providerMissing || m.hostedSender() == nil {
		fail()
		return
	}
	projection, ok := m.threadProjection(s, payload, page, &epoch)
	if !expanding && !ok {
		fail()
		return
	}
	if s.hosted == nil {
		m.initHosted(ctx, s)
	}
	in := s.hosted.inbox
	m.hostedMu.Lock()
	j, joined := in.threadJoins[*p.Conversation]
	initializing := !page && !expanding && *version == 0
	if in.query != nil || !expanding && !joined && len(in.threadJoins) >= maxThreadJoins || !expanding && joined && !initializing && j.projection != projection {
		m.hostedMu.Unlock()
		fail()
		return
	}
	queryCtx, cancel := context.WithCancel(in.ctx)
	q := &threadQuery{id: *id, conversation: *p.Conversation, ctx: queryCtx, cancel: cancel, inbox: in, body: expansion, children: children}
	if !expanding {
		q.projection = projection
	}
	if expansion != nil {
		expansion.Limit = limit
	}
	if page {
		q.page = &protocol.ThreadPagePayload{ConversationID: *p.Conversation, BeforeOrder: *beforeOrder, Limit: limit, Projection: projection}
		if projection != "" {
			q.page.Epoch = *epoch
		}
	} else if !expanding {
		q.request = protocol.ThreadCatchUpPayload{ConversationID: *p.Conversation, Epoch: *epoch, Version: *version, Limit: limit, Projection: projection}
	}
	in.query = q
	m.hostedMu.Unlock()
	m.enqueueAppFrame(ctx, s, appFrameJob{kind: appFrameThreadQuery, query: q})
}

// threadProjection validates an optional catch-up/page projection after the
// normal gates. Only the compact literal is known; it needs the negotiated
// capability, both compact providers and, for pages, a nonempty epoch.
func (m *V2SessionManager) threadProjection(s *V2Session, payload map[string]json.RawMessage, page bool, epoch **string) (string, bool) {
	raw, present := payload["projection"]
	if !present {
		return "", true
	}
	var projection *string
	if json.Unmarshal(raw, &projection) != nil || projection == nil || *projection != protocol.ThreadProjectionCompact || !s.threadCompact || m.cfg.ThreadCompactCatchUp == nil || m.cfg.ThreadCompactPage == nil {
		return "", false
	}
	if page && (json.Unmarshal(payload["epoch"], epoch) != nil || *epoch == nil || **epoch == "") {
		return "", false
	}
	return *projection, true
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
	if q.projection != "" {
		m.runCompactThreadQuery(s, q, fail)
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
			p.Items = append(p.Items, threadItemFrom(i))
		}
		reply = p
	}
	m.sendThreadReply(s, q, env, reply, fail)
}

func threadItemFrom(i thread.Item) protocol.ThreadItem {
	return protocol.ThreadItem{ID: i.ID, Kind: i.Kind, Order: i.Order, Rev: i.Rev, EndedOrder: i.EndedOrder, Session: i.Session, Agent: i.Agent, NoChild: i.NoChild, Turn: i.Turn, Parent: i.Parent, Status: i.Status, Active: i.Active, Shown: i.Shown, Summary: i.Summary, Subtype: i.Subtype, Content: i.Content}
}

// threadSummaryFrom copies every header fact, including unknown (nil) ones.
func threadSummaryFrom(row thread.ChildSummary) protocol.ThreadSummary {
	return protocol.ThreadSummary{ID: row.ID, Kind: row.Kind, Order: row.Order, Rev: row.Rev, EndedOrder: row.EndedOrder, Session: row.Session, Agent: row.Agent, NoChild: row.NoChild, Turn: row.Turn, Parent: row.Parent, Status: row.Status, Active: row.Active, Shown: row.Shown, Headline: row.Headline, Subtype: row.Subtype, DetailOmitted: true,
		TaskID: row.TaskID, ErrorSummary: row.ErrorSummary, PublicChildCount: row.PublicChildCount, HasPublicChildren: row.HasPublicChildren, SubtreeVersion: row.SubtreeVersion}
}

// runCompactThreadQuery answers a compact query with the complete reply's bounds
// and final semantics. A Store reset, or a page captured in another epoch than
// requested, becomes a compact-scoped reset; a mislabelled result refuses.
func (m *V2SessionManager) runCompactThreadQuery(s *V2Session, q *threadQuery, fail func()) {
	var result thread.CompactQueryResult
	var err error
	if q.page != nil {
		result, err = m.cfg.ThreadCompactPage(q.ctx, *q.page)
	} else {
		result, err = m.cfg.ThreadCompactCatchUp(q.ctx, q.request)
	}
	if err != nil || result.Err != nil || result.State != thread.StateUsable || q.ctx.Err() != nil || result.Projection != q.projection || string(result.ConversationID) != q.conversation {
		fail()
		return
	}
	env := protocol.Envelope{TS: time.Now().UTC(), InReplyTo: &q.id}
	if result.ResetRequired || q.page != nil && result.Epoch != q.page.Epoch {
		m.sendThreadReply(s, q, env, protocol.ThreadResetPayload{ConversationID: q.conversation, Epoch: result.Epoch, Version: result.Version, Projection: q.projection}, fail)
		return
	}
	p := protocol.ThreadItemsPayload{ConversationID: q.conversation, Epoch: result.Epoch, Version: result.Version, Final: true, Projection: q.projection}
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
		p.Items = append(p.Items, threadItemFrom(i))
	}
	for _, h := range result.Headers {
		if q.ctx.Err() != nil {
			fail()
			return
		}
		p.Headers = append(p.Headers, threadSummaryFrom(h))
	}
	m.sendThreadReply(s, q, env, p, fail)
}

// sendThreadReply encodes and delivers one correlated reply in order; any
// failure or cancellation ends with the content-free refusal.
func (m *V2SessionManager) sendThreadReply(s *V2Session, q *threadQuery, env protocol.Envelope, reply any, fail func()) {
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

// cancelThreadQuery admits cancellation under hostedMu then pushMu, so a
// quarantined update cannot cancel an already-selected query delivery. A live
// update cancels only a query of its own projection that has not captured it;
// threadQueryGate withholds covered updates and loads of the other projection.
func (m *V2SessionManager) cancelThreadQuery(connID string, env protocol.Envelope) {
	compact := env.Type == protocol.TypeThreadCompactUpdate
	if !compact && (!threadItemUpdate(env.Type) || env.InReplyTo != nil) {
		return
	}
	conversation := pushedConversationID(env)
	m.hostedMu.Lock()
	defer m.hostedMu.Unlock()
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	if q := m.queues[connID]; q != nil && q.overflowed {
		return
	}
	in := m.hostedInboxes[connID]
	if in == nil || in.query == nil || in.query.conversation != conversation || in.query.coversLive(env) {
		return
	}
	if compact && in.query.projection == protocol.ThreadProjectionCompact || !compact && in.query.projection == "" {
		in.query.cancel()
	}
}

// coversLive identifies delayed complete-view publications already represented
// by the selected reply. It does not establish a completed client join.
func (q *threadQuery) coversLive(env protocol.Envelope) bool {
	if q.captured == nil || q.projection != "" || !threadItemUpdate(env.Type) || env.InReplyTo != nil {
		return false
	}
	var p struct {
		Conversation string `json:"conversation_id"`
		Epoch        string `json:"epoch"`
		Version      uint64 `json:"version"`
	}
	return json.Unmarshal(env.Payload, &p) == nil && p.Conversation == q.conversation &&
		p.Epoch == q.captured.epoch && p.Version <= q.captured.version
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
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	if q := m.queues[s.connID]; q != nil && q.overflowed {
		return ErrConnNotFound
	}
	in := s.hosted.inbox
	if env.Type == protocol.TypeThreadCompactUpdate {
		return in.compactLiveGate(env)
	}
	if threadItemUpdate(env.Type) && env.InReplyTo == nil {
		var p struct {
			Conversation string `json:"conversation_id"`
			Epoch        string `json:"epoch"`
			Version      uint64 `json:"version"`
		}
		if json.Unmarshal(env.Payload, &p) != nil {
			return ErrThreadWithheld
		}
		if q := in.query; q != nil && q.conversation == p.Conversation {
			if q.coversLive(env) {
				return ErrThreadWithheld
			}
			// A compact load owns its conversation's view from admission, so complete
			// rows are withheld rather than mixed in; they never cancel it.
			if q.projection != "" {
				return ErrThreadWithheld
			}
			q.cancel()
		}
		if j, ok := in.threadJoins[p.Conversation]; ok && (j.projection != "" || j.epoch == p.Epoch && p.Version <= j.version) {
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
	// Only shared fields are needed: continuation is numeric on page markers
	// and an object on fragmented rows. Fragment completion is not query finality.
	var p struct {
		Epoch      string `json:"epoch"`
		Version    uint64 `json:"version"`
		Projection string `json:"projection"`
		Final      bool   `json:"final"`
	}
	if env.Type == protocol.TypeThreadItems && json.Unmarshal(env.Payload, &p) == nil {
		if err := o.query.ctx.Err(); err != nil {
			return err
		}
		if o.query.projection == "" && p.Epoch != "" {
			o.query.captured = &threadJoin{epoch: p.Epoch, version: p.Version}
		}
		if !p.Final {
			return nil
		}
		if in.threadJoins == nil {
			in.threadJoins = make(map[string]threadJoin)
		}
		in.threadJoins[o.query.conversation] = threadJoin{p.Epoch, p.Version, p.Projection}
		in.query = nil
	}
	return nil
}

// compactLiveGate admits one compact live frame, or continuation part, under
// hostedMu. It is checked whatever its InReplyTo, since compact updates are never
// replies. A complete load in flight owns its conversation's view, so the frame is
// withheld without cancelling it; a compact load is cancelled as complete live
// cancels a complete one. The frame then needs a compact join of its epoch below
// its version. It never writes a join: only a successful query final does, so
// updates for different roots at one version are not fenced against each other.
func (in *hostedInbox) compactLiveGate(env protocol.Envelope) error {
	var p struct {
		Projection   string `json:"projection"`
		Conversation string `json:"conversation_id"`
		Epoch        string `json:"epoch"`
		Version      uint64 `json:"version"`
	}
	if json.Unmarshal(env.Payload, &p) != nil || p.Projection != protocol.ThreadProjectionCompact {
		return ErrThreadWithheld
	}
	if q := in.query; q != nil && q.conversation == p.Conversation && q.body == nil && !q.children {
		if q.projection != protocol.ThreadProjectionCompact {
			return ErrThreadWithheld
		}
		q.cancel()
	}
	if j, ok := in.threadJoins[p.Conversation]; !ok || j.projection != protocol.ThreadProjectionCompact || j.epoch != p.Epoch || p.Version <= j.version {
		return ErrThreadWithheld
	}
	return nil
}

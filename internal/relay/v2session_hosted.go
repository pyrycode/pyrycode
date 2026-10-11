package relay

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"sort"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// HostedAppsDiscovery returns an atomic detached committed host snapshot.
// List may block; relay retains its read capacity until it returns.
type HostedAppsDiscovery interface {
	List() ([]apps.Record, uint64)
}

const hostedQueueLimit = 16
const hostedDeliveryTimeout = 5 * time.Second

type hostedWalk struct {
	token, host, last string
	revision          uint64
	expires           time.Time
}
type hostedPage struct {
	id      uint64
	retired bool
	cursor  *hostedWalk
}
type hostedOutput struct {
	unsolicited  bool
	typ          string
	payload      any
	reply        uint64
	page         *hostedPage
	wire         *protocol.RoutingEnvelope
	env          *protocol.Envelope
	newSend      *noise.CipherState
	deliveryDone <-chan error
}
type hostedState struct {
	inbox       *hostedInbox
	deliveryCtx context.Context
	pending     *hostedPage
	reading     bool
	walk        *hostedWalk
	queue       []*hostedOutput
	active      *hostedOutput
	cancel      context.CancelFunc
	rekey       bool
}
type hostedResult struct {
	s        *V2Session
	page     *hostedPage
	records  []apps.Record
	revision uint64
	output   *hostedOutput
	err      error
}

func (m *V2SessionManager) routeHosted(ctx context.Context, s *V2Session, raw []byte) bool {
	req, recognized, correlated, valid := parseHostedRequest(raw)
	if !recognized {
		return false
	}
	m.drainHostedDelivery(ctx, s)
	if s.state != V2StateOpen {
		return true
	}
	if !m.hostedAuthorized(s) {
		m.closeWith(ctx, s, StatusUnauthorized, nil)
		return true
	}
	if s.hosted == nil {
		m.initHosted(ctx, s)
	}
	if !correlated {
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
		return true
	}
	if !valid {
		m.hostedError(ctx, s, req.id, "app.invalid_request", false)
		return true
	}
	if !s.hostedApps {
		m.hostedError(ctx, s, req.id, protocol.CodeProtocolUnsupported, false)
		return true
	}
	h := s.hosted
	if req.typ == protocol.TypeAppCancel {
		if req.cancel.AppID != "" {
			m.hostedError(ctx, s, req.id, protocol.CodeProtocolUnsupported, false)
			return true
		}
		if h.pending != nil && h.pending.id == req.cancel.RequestID && !h.pending.retired {
			h.pending.retired = true
			h.walk = nil
			if h.active != nil && h.active.page == h.pending {
				m.closeWith(ctx, s, StatusProtocolMismatch, nil)
				return true
			}
			kept := h.queue[:0]
			for _, o := range h.queue {
				if o.page != h.pending {
					kept = append(kept, o)
				}
			}
			m.releaseHosted(s, len(h.queue)-len(kept))
			clear(h.queue[len(kept):])
			h.queue = kept
			if !h.reading {
				h.pending = nil
			}
		}
		m.hostedEnqueue(ctx, s, &hostedOutput{typ: protocol.TypeAppCancelled, payload: req.cancel, reply: req.id})
		return true
	}
	if h.pending != nil || h.reading {
		m.hostedError(ctx, s, req.id, "app.busy", true)
		return true
	}
	var cursor *hostedWalk
	if req.cursor != "" {
		cursor = h.walk
		if cursor == nil || cursor.host != m.cfg.ServerID || cursor.token != req.cursor {
			m.hostedError(ctx, s, req.id, "app.invalid_request", false)
			return true
		}
		if !m.hostedNow().Before(cursor.expires) {
			h.walk = nil
			m.hostedError(ctx, s, req.id, "apps.changed", true)
			return true
		}
	} else {
		h.walk = nil
	}
	page := &hostedPage{id: req.id, cursor: cursor}
	h.pending = page
	h.reading = true
	go func() {
		defer m.cfg.Logger.Debug("relay: hosted reader exited", "event", "v2.hosted.reader_exited", "conn_id", s.connID)
		records, revision := m.cfg.HostedAppsDiscovery.List()
		result := hostedResult{s: s, page: page, records: records, revision: revision}
		select {
		case m.hostedDone <- result:
		case <-ctx.Done():
		case <-s.done:
		}
	}()
	return true
}
func (m *V2SessionManager) hostedAuthorized(s *V2Session) bool {
	if s.device == nil {
		return false
	}
	_, ok := m.cfg.Devices.FindByTokenHash(s.device.TokenHash)
	return ok
}
func (m *V2SessionManager) hostedError(ctx context.Context, s *V2Session, id uint64, code string, retry bool) {
	p := struct {
		Code       string `json:"code"`
		Message    string `json:"message"`
		Retryable  bool   `json:"retryable"`
		RetryAfter int    `json:"retry_after_s,omitempty"`
	}{code, "invalid hosted app request", retry, 0}
	switch code {
	case protocol.CodeProtocolUnsupported:
		p.Message = "hosted app operation is unsupported"
	case "app.busy":
		p.Message = "app request capacity is full"
		p.RetryAfter = 1
	case "apps.changed":
		p.Message = "app discovery changed; restart the list"
	}
	m.hostedEnqueue(ctx, s, &hostedOutput{typ: protocol.TypeError, payload: p, reply: id})
}
func (m *V2SessionManager) hostedEnqueue(ctx context.Context, s *V2Session, o *hostedOutput) {
	h := s.hosted
	if s.state != V2StateOpen {
		return
	}
	m.takeHostedChanges(ctx, s)
	m.hostedMu.Lock()
	in := h.inbox
	full := in.retired || in.overflow || in.used >= hostedQueueLimit
	if !full {
		in.used++
	}
	m.hostedMu.Unlock()
	if full {
		m.closeWith(ctx, s, StatusQueueOverflow, nil)
		return
	}
	h.queue = append(h.queue, o)
	m.cfg.Logger.Debug("relay: hosted output queued", "event", "v2.hosted.queued", "conn_id", s.connID, "type", o.typ)
	m.hostedDeliver(ctx, s)
}
func (m *V2SessionManager) hostedDeliver(ctx context.Context, s *V2Session) {
	h := s.hosted
	if s.state != V2StateOpen {
		return
	}
	m.takeHostedChanges(ctx, s)
	if h.active != nil || len(h.queue) == 0 || s.state != V2StateOpen {
		return
	}
	send := m.hostedSender()
	if send == nil {
		m.closeWith(ctx, s, StatusSessionGone, nil)
		return
	}
	if !m.hostedAuthorized(s) {
		m.closeWith(ctx, s, StatusUnauthorized, nil)
		return
	}
	if m.transportDown() {
		m.closeWith(ctx, s, StatusSessionGone, nil)
		return
	}
	o := h.queue[0]
	h.queue[0] = nil
	h.queue = h.queue[1:]
	wire := protocol.RoutingEnvelope{ConnID: s.connID}
	if o.wire != nil {
		wire = *o.wire
		if o.newSend != nil {
			s.send = o.newSend
		}
	} else {
		if s.nextID > hostedMaxID {
			m.closeWith(ctx, s, StatusSessionGone, nil)
			return
		}
		var env protocol.Envelope
		if o.env != nil {
			env = *o.env
		} else {
			payload, err := json.Marshal(o.payload)
			if err != nil {
				m.closeWith(ctx, s, StatusProtocolMismatch, nil)
				return
			}
			env = protocol.Envelope{Type: o.typ, TS: time.Now().UTC(), Payload: payload}
			if !o.unsolicited {
				env.InReplyTo = &o.reply
			}
		}
		if s.nextID < 2 {
			s.nextID = 2
		}
		env.ID = s.nextID
		raw, err := json.Marshal(env)
		s.nextID++
		if err != nil {
			m.closeWith(ctx, s, StatusProtocolMismatch, nil)
			return
		}
		sealed, err := s.send.Encrypt(raw)
		if err != nil {
			m.closeWith(ctx, s, StatusProtocolMismatch, nil)
			return
		}
		wire.Frame, err = marshalInnerFrameV2(protocol.TypeNoiseMsg, sealed)
		if err != nil {
			m.closeWith(ctx, s, StatusProtocolMismatch, nil)
			return
		}
	}
	h.active = o
	deliveryCtx, cancel := context.WithTimeout(h.inbox.ctx, hostedDeliveryTimeout)
	h.cancel = cancel
	h.deliveryCtx = deliveryCtx
	deliveryDone := make(chan error, 1)
	o.deliveryDone = deliveryDone
	go func() {
		deliveryDone <- send(deliveryCtx, wire)
		select {
		case m.hostedDeliveryDone <- struct{}{}:
		default:
		}
	}()
}

func (m *V2SessionManager) hostedSender() func(context.Context, protocol.RoutingEnvelope) error {
	if m.cfg.HostedAppsSend != nil {
		return m.cfg.HostedAppsSend
	}
	return m.cfg.OutboundContext
}

// drainHostedDelivery observes a returned delivery before admitting later work.
// Coalesced wakeups do not retain completion goroutines or delay finished pages.
func (m *V2SessionManager) drainHostedDelivery(ctx context.Context, s *V2Session) {
	if s.hosted == nil || s.hosted.active == nil {
		return
	}
	o := s.hosted.active
	select {
	case err := <-o.deliveryDone:
		m.handleHostedDone(ctx, hostedResult{s: s, output: o, err: err})
	default:
	}
}
func (m *V2SessionManager) handleHostedDone(ctx context.Context, res hostedResult) {
	s := res.s
	kind := "delivery"
	if res.page != nil {
		kind = "read"
	}
	defer m.cfg.Logger.Debug("relay: hosted operation completed", "event", "v2.hosted.completed", "conn_id", s.connID, "kind", kind)
	if s.state != V2StateOpen || m.sessions[s.connID] != s {
		return
	}
	h := s.hosted
	if !m.hostedAuthorized(s) {
		m.closeWith(ctx, s, StatusUnauthorized, nil)
		return
	}
	if res.output != nil {
		if h.active != res.output {
			return
		}
		h.cancel()
		h.cancel = nil
		h.deliveryCtx = nil
		h.active = nil
		m.releaseHosted(s, 1)
		if res.err != nil {
			m.closeWith(ctx, s, StatusSessionGone, nil)
			return
		}
		if res.output.page != nil && h.pending == res.output.page {
			h.pending = nil
		}
		if res.output.newSend != nil {
			h.rekey = false
			s.rekeyComplete(m, ctx)
		}
		if res.output.env != nil && res.output.env.Type == protocol.TypeRekeyRequest {
			s.rekeyReplyTimer = m.armRekeyReplyTimer(ctx, s)
		}
		m.hostedDeliver(ctx, s)
		return
	}
	if h.pending != res.page {
		return
	}
	h.reading = false
	if res.page.retired {
		h.pending = nil
		return
	}
	cursor := res.page.cursor
	if cursor != nil && (cursor.revision != res.revision || !m.hostedNow().Before(cursor.expires)) {
		h.pending = nil
		h.walk = nil
		m.hostedError(ctx, s, res.page.id, "apps.changed", true)
		return
	}
	sort.Slice(res.records, func(i, j int) bool { return res.records[i].AppID < res.records[j].AppID })
	payload := protocol.AppsPayload{Revision: res.revision, Items: []protocol.HostedAppRecord{}}
	last := ""
	if cursor != nil {
		last = cursor.last
	}
	more := false
	for _, r := range res.records {
		if r.AppID <= last {
			continue
		}
		if len(payload.Items) == 16 {
			more = true
			break
		}
		item := hostedRecord(r)
		payload.Items = append(payload.Items, item)
		last = r.AppID
	}
	h.walk = nil
	if more {
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			h.pending = nil
			m.closeWith(ctx, s, StatusSessionGone, nil)
			return
		}
		token := base64.RawURLEncoding.EncodeToString(random[:])
		h.walk = &hostedWalk{token: token, host: m.cfg.ServerID, last: last, revision: res.revision, expires: m.hostedNow().Add(30 * time.Second)}
		payload.NextCursor = &token
	}
	m.hostedEnqueue(ctx, s, &hostedOutput{typ: protocol.TypeApps, payload: payload, reply: res.page.id, page: res.page})
}

func (m *V2SessionManager) hostedNow() time.Time {
	if m.cfg.hostedNow != nil {
		return m.cfg.hostedNow()
	}
	return time.Now()
}

// hostedEnvelope admits plaintext before sealing. IDs and Noise nonces are
// allocated only when the ordered lane selects this envelope for delivery.
func (m *V2SessionManager) hostedEnvelope(ctx context.Context, s *V2Session, env protocol.Envelope) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.hostedEnqueue(ctx, s, &hostedOutput{env: &env})
}
func (m *V2SessionManager) retireHosted(s *V2Session) {
	if h := s.hosted; h != nil {
		m.hostedMu.Lock()
		if in := h.inbox; in != nil {
			in.retired = true
			in.pending = nil
			in.cancel()
			if m.hostedInboxes[s.connID] == in {
				delete(m.hostedInboxes, s.connID)
			}
		}
		m.hostedMu.Unlock()
		h.walk = nil
		if h.pending != nil {
			h.pending.retired = true
		}
		h.queue = nil
		if h.cancel != nil {
			h.cancel()
			h.cancel = nil
		}
	}
}

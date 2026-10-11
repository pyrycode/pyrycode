package relay

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"sort"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
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
	typ     string
	payload any
	reply   uint64
	page    *hostedPage
	wire    *protocol.RoutingEnvelope
}
type hostedState struct {
	deliveryCtx context.Context
	pending     *hostedPage
	reading     bool
	walk        *hostedWalk
	queue       []*hostedOutput
	active      *hostedOutput
	cancel      context.CancelFunc
	nextID      uint64
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
	if !m.hostedAuthorized(s) {
		m.closeWith(ctx, s, StatusUnauthorized, nil)
		return true
	}
	if s.hosted == nil {
		s.hosted = &hostedState{nextID: 2}
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
		if !time.Now().Before(cursor.expires) {
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
	count := len(h.queue)
	if h.active != nil {
		count++
	}
	if count >= hostedQueueLimit {
		m.closeWith(ctx, s, StatusQueueOverflow, nil)
		return
	}
	h.queue = append(h.queue, o)
	m.hostedDeliver(ctx, s)
}
func (m *V2SessionManager) hostedDeliver(ctx context.Context, s *V2Session) {
	h := s.hosted
	if h.active != nil || len(h.queue) == 0 || s.state != V2StateOpen {
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
	h.queue = h.queue[1:]
	wire := protocol.RoutingEnvelope{ConnID: s.connID}
	if o.wire != nil {
		wire = *o.wire
	} else {
		if h.nextID > hostedMaxID {
			m.closeWith(ctx, s, StatusSessionGone, nil)
			return
		}
		payload, err := json.Marshal(o.payload)
		if err != nil {
			m.closeWith(ctx, s, StatusProtocolMismatch, nil)
			return
		}
		raw, err := json.Marshal(protocol.Envelope{ID: h.nextID, Type: o.typ, TS: time.Now().UTC(), Payload: payload, InReplyTo: &o.reply})
		h.nextID++
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
	deliveryCtx, cancel := context.WithTimeout(ctx, hostedDeliveryTimeout)
	h.cancel = cancel
	h.deliveryCtx = deliveryCtx
	send := m.cfg.HostedAppsSend
	// Unsupported peers also need a cancellable control lane. With no installed
	// adapter the legacy callback is isolated to this one worker; it is never replaced.
	go func() {
		var err error
		if send != nil {
			err = send(deliveryCtx, wire)
		} else {
			err = m.cfg.Outbound(wire)
		}
		select {
		case m.hostedDone <- hostedResult{s: s, output: o, err: err}:
		case <-ctx.Done():
		case <-s.done:
		}
	}()
}
func (m *V2SessionManager) handleHostedDone(ctx context.Context, res hostedResult) {
	s := res.s
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
		if res.err != nil {
			m.closeWith(ctx, s, StatusSessionGone, nil)
			return
		}
		if res.output.page != nil && h.pending == res.output.page {
			h.pending = nil
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
	if cursor != nil && (cursor.revision != res.revision || !time.Now().Before(cursor.expires)) {
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
		item := protocol.HostedAppRecord{AppID: r.AppID, Title: r.Title, Desired: r.Desired, State: r.State, Revision: r.Revision}
		if r.ActiveRelease != "" {
			v := r.ActiveRelease
			item.ActiveRelease = &v
		}
		if r.PendingRelease != "" {
			v := r.PendingRelease
			item.PendingRelease = &v
		}
		if r.LastError != nil {
			item.LastError = &struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}{r.LastError.Code, r.LastError.Message}
		}
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
		h.walk = &hostedWalk{token: token, host: m.cfg.ServerID, last: last, revision: res.revision, expires: time.Now().Add(30 * time.Second)}
		payload.NextCursor = &token
	}
	m.hostedEnqueue(ctx, s, &hostedOutput{typ: protocol.TypeApps, payload: payload, reply: res.page.id, page: res.page})
}
func (m *V2SessionManager) retireHosted(s *V2Session) {
	if h := s.hosted; h != nil {
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

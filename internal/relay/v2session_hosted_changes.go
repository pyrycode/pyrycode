package relay

import (
	"context"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// hostedInbox is guarded by hostedMu. Reservations include callback handoff,
// Run's queue and active delivery. Each session owns a distinct inbox.
type hostedInbox struct {
	pending                       []*hostedOutput
	used                          int
	subscribed, retired, overflow bool
	ctx                           context.Context
	cancel                        context.CancelFunc
	capacity                      chan struct{}
}
type hostedClose struct{ cancel context.CancelFunc }

// HostedAppsChanged consumes current-process committed registry changes. It
// returns without peer I/O, registry re-entry or worker creation. Subscribe
// supplies increasing host revisions; subscription ownership stays with callers.
func (m *V2SessionManager) HostedAppsChanged(change apps.Change) {
	if change.ServerID != m.cfg.ServerID {
		return
	}
	typ, payload := protocol.TypeAppRemoved, any(protocol.AppRemovedPayload{AppID: change.AppID, Revision: change.Revision})
	if change.Record != nil {
		typ, payload = protocol.TypeAppUpdated, hostedRecord(*change.Record)
	}
	m.hostedMu.Lock()
	defer m.hostedMu.Unlock()
	if m.hostedStopped {
		return
	}
	for _, in := range m.hostedInboxes {
		if !in.subscribed || in.retired || in.overflow {
			continue
		}
		if in.used == hostedQueueLimit {
			in.overflow = true
			in.pending = nil
			in.cancel()
		} else {
			in.used++
			in.pending = append(in.pending, &hostedOutput{typ: typ, payload: payload, unsolicited: true})
		}
	}
	select {
	case m.hostedChanges <- struct{}{}:
	default:
	}
}
func (m *V2SessionManager) initHosted(ctx context.Context, s *V2Session) {
	inCtx, cancel := context.WithCancel(ctx)
	in := &hostedInbox{subscribed: s.hostedApps, ctx: inCtx, cancel: cancel, capacity: make(chan struct{})}
	s.hosted = &hostedState{inbox: in}
	m.hostedMu.Lock()
	defer m.hostedMu.Unlock()
	if m.hostedInboxes == nil {
		m.hostedInboxes = make(map[string]*hostedInbox)
	}
	if old := m.hostedInboxes[s.connID]; old != nil {
		old.cancel()
	}
	m.hostedInboxes[s.connID] = in
}
func (m *V2SessionManager) cancelHostedClose(connID string) {
	m.hostedMu.Lock()
	defer m.hostedMu.Unlock()
	if old := m.hostedCloses[connID]; old != nil {
		old.cancel()
		delete(m.hostedCloses, connID)
	}
}
func (m *V2SessionManager) drainHostedChanges(ctx context.Context) {
	for _, s := range m.sessions {
		if s.hosted == nil {
			continue
		}
		m.takeHostedChanges(ctx, s)
		m.hostedDeliver(ctx, s)
	}
}
func (m *V2SessionManager) takeHostedChanges(ctx context.Context, s *V2Session) {
	in := s.hosted.inbox
	m.hostedMu.Lock()
	pending, overflow := in.pending, in.overflow
	in.pending = nil
	m.hostedMu.Unlock()
	if overflow {
		m.closeWith(ctx, s, StatusQueueOverflow, nil)
		return
	}
	s.hosted.queue = append(s.hosted.queue, pending...)
}
func (m *V2SessionManager) releaseHosted(s *V2Session, count int) {
	m.hostedMu.Lock()
	defer m.hostedMu.Unlock()
	if in := s.hosted.inbox; !in.retired {
		in.used -= count
		close(in.capacity)
		in.capacity = make(chan struct{})
		select {
		case m.drainCh <- struct{}{}:
		default:
		}
	}
}
func (m *V2SessionManager) stopHosted() {
	m.hostedMu.Lock()
	defer m.hostedMu.Unlock()
	m.hostedStopped = true
	for _, in := range m.hostedInboxes {
		in.retired = true
		in.pending = nil
		in.cancel()
	}
	for _, close := range m.hostedCloses {
		close.cancel()
	}
	m.hostedInboxes = nil
	m.hostedCloses = nil
}

// hostedCloseContext fences terminal sends against replacement generations.
// The sender must honor cancellation through actual publication.
func (m *V2SessionManager) hostedCloseContext(ctx context.Context, s *V2Session) (context.Context, func()) {
	closeCtx, cancel := context.WithTimeout(ctx, hostedDeliveryTimeout)
	close := &hostedClose{cancel: cancel}
	m.hostedMu.Lock()
	if m.hostedCloses == nil {
		m.hostedCloses = make(map[string]*hostedClose)
	}
	m.hostedCloses[s.connID] = close
	m.hostedMu.Unlock()
	return closeCtx, func() {
		cancel()
		m.hostedMu.Lock()
		if m.hostedCloses[s.connID] == close {
			delete(m.hostedCloses, s.connID)
		}
		m.hostedMu.Unlock()
	}
}
func hostedRecord(r apps.Record) protocol.HostedAppRecord {
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
	return item
}

// hostedPushAvailable is only a wake hint; reserveHostedPush performs admission.
// Both run off pushMu so callback and producer locks are never nested.
func (m *V2SessionManager) hostedPushAvailable(s *V2Session) bool {
	if s == nil || s.hosted == nil {
		return true
	}
	m.hostedMu.Lock()
	defer m.hostedMu.Unlock()
	in := s.hosted.inbox
	return !in.retired && !in.overflow && in.used < hostedQueueLimit
}

// reserveHostedPush keeps a slot from being stolen by a callback between the
// upstream pop and hosted admission. Only Run owns pushReserved.
func (m *V2SessionManager) reserveHostedPush(s *V2Session) bool {
	if s == nil || s.hosted == nil {
		return true
	}
	m.hostedMu.Lock()
	defer m.hostedMu.Unlock()
	in := s.hosted.inbox
	if in.retired || in.overflow || in.used >= hostedQueueLimit {
		return false
	}
	in.used++
	s.hosted.pushReserved = true
	return true
}

func (m *V2SessionManager) releaseHostedPush(s *V2Session) {
	if s != nil && s.hosted != nil && s.hosted.pushReserved {
		s.hosted.pushReserved = false
		m.releaseHosted(s, 1)
	}
}

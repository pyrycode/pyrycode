//go:build permission_observe

package relay

import (
	"encoding/json"
	"sync/atomic"

	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type permissionObserver struct {
	recorder atomic.Pointer[permbridge.ObservationRecorder]
}
type permissionQueueObserver struct {
	recorder   *permbridge.ObservationRecorder
	connection string
}
type permissionObservation struct {
	recorder *permbridge.ObservationRecorder
	identity permbridge.ObservationIdentity
	stage    string
}

// ObservePermissions attaches diagnostic queue evidence. Attach before activity
// for complete coverage. nil disables future pushes; retained entries keep origins.
// Dismissals lack a class; only a modal join to a permission show proves membership.
func (m *V2SessionManager) ObservePermissions(o *permbridge.ObservationRecorder) {
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	if old := m.permissionObserver.recorder.Load(); old != nil && old != o {
		old.MarkIncomplete()
	}
	if m.permissionObserver.recorder.Load() == o {
		return
	}
	m.permissionObserver.recorder.Store(o)
	for _, q := range m.queues {
		if o != nil {
			o.MarkIncomplete()
		}
		q.permissionObserver = m.newPermissionQueue()
	}
}

func (m *V2SessionManager) newPermissionQueue() permissionQueueObserver {
	o := m.permissionObserver.recorder.Load()
	if o == nil {
		return permissionQueueObserver{}
	}
	return permissionQueueObserver{o, o.NewConnectionIdentity().Connection}
}

func permissionTag(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func (m *V2SessionManager) permissionAttempt(q *pushQueue, e protocol.Envelope, r *LiveState) permissionObservation {
	observer := permissionQueueObserver{recorder: m.permissionObserver.recorder.Load()}
	if q != nil {
		observer = q.permissionObserver
	}
	o := observer.recorder
	if o == nil || e.SessionStateCleared || (e.Type != protocol.TypeModalShown && e.Type != protocol.TypeModalDismissed) {
		return permissionObservation{}
	}
	var p struct{ Class, ModalID, ConversationID json.RawMessage }
	// Only these fields are decoded; content fields never enter observation state.
	var fields struct {
		Class          json.RawMessage `json:"class"`
		ModalID        json.RawMessage `json:"modal_id"`
		ConversationID json.RawMessage `json:"conversation_id"`
	}
	if json.Unmarshal(e.Payload, &fields) == nil {
		p.Class = fields.Class
		p.ModalID = fields.ModalID
		p.ConversationID = fields.ConversationID
	}
	if e.Type == protocol.TypeModalShown && permissionTag(p.Class) != "permission" {
		return permissionObservation{}
	}
	conversation := permissionTag(e.ConversationID)
	if len(e.ConversationID) == 0 {
		conversation = permissionTag(p.ConversationID)
	}
	source := ""
	if r != nil {
		conversation = r.ConversationID
		// Bound before encoding; oversize goes through Identity's explicit loss path.
		if len(conversation)+len(r.ReadingID) > permbridge.ObservationMaxIdentityInput {
			source = string(make([]byte, permbridge.ObservationMaxIdentityInput+1))
		} else {
			raw, _ := json.Marshal([]any{conversation, r.SessionGeneration, r.ReadingID, r.Revision, e.Type})
			source = string(raw)
		}
	}
	id := o.Identity(conversation, permissionTag(e.SessionID), "", permissionTag(p.ModalID), "", source)
	id.Connection = observer.connection
	return permissionObservation{o, id, "queue_" + e.Type}
}

func (p permissionObservation) record(reason string) {
	if p.recorder != nil {
		p.recorder.Submit(p.stage, reason, p.identity)
	}
}

func (q *pushQueue) observePermissions(reason string) {
	for _, item := range q.items {
		item.permissionObservation.record(reason)
	}
}

func (m *V2SessionManager) observePermissionCancellation(connID string, e protocol.Envelope, r *LiveState) {
	if !m.pushMu.TryLock() {
		if o := m.permissionObserver.recorder.Load(); o != nil {
			o.MarkIncomplete()
		}
		return
	}
	defer m.pushMu.Unlock()
	m.permissionAttempt(m.queues[connID], e, r).record("cancelled")
}

// observePermissionHold preserves the drain's unlocked hold paths. Contention
// loses coverage explicitly instead of adding a queue-lock wait to a held lane.
func (m *V2SessionManager) observePermissionHold(connID, reason string) {
	if !m.pushMu.TryLock() {
		if o := m.permissionObserver.recorder.Load(); o != nil {
			o.MarkIncomplete()
		}
		return
	}
	defer m.pushMu.Unlock()
	if q := m.queues[connID]; q != nil {
		if q.overflowed {
			reason = "quarantined"
		}
		q.observePermissions(reason)
	}
}

func (m *V2SessionManager) observePermissionTransportHold() {
	if !m.pushMu.TryLock() {
		if o := m.permissionObserver.recorder.Load(); o != nil {
			o.MarkIncomplete()
		}
		return
	}
	defer m.pushMu.Unlock()
	for _, q := range m.queues {
		reason := "hold_transport"
		if q.overflowed {
			reason = "quarantined"
		}
		q.observePermissions(reason)
	}
}

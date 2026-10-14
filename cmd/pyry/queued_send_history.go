package main

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	historySendAccepted  = "send_accepted"
	historySendDelivered = "send_delivered"
	historySendDropped   = "send_dropped"
	historySendLost      = "send_lost"
)

type queuedSendFact struct {
	ConversationID  string    `json:"conversation_id"`
	DeviceID        string    `json:"device_id,omitempty"`
	MessageID       string    `json:"message_id,omitempty"`
	QueuedMessageID uint64    `json:"queued_msg_id,omitempty"`
	Text            string    `json:"text,omitempty"`
	AttachmentIDs   []string  `json:"attachment_ids,omitempty"`
	AcceptedAt      time.Time `json:"accepted_at,omitzero"`
	ClientSentAt    string    `json:"client_sent_at,omitempty"`
	AcceptedEntryID uint64    `json:"accepted_entry_id,omitempty"`
	DeliveryEntryID uint64    `json:"delivery_entry_id,omitempty"`
	OccurredAt      time.Time `json:"occurred_at,omitzero"`
	Reason          string    `json:"reason,omitempty"`
}

func queuedSendHistoryType(typ string) bool {
	switch typ {
	case historySendAccepted, historySendDelivered, historySendDropped, historySendLost:
		return true
	default:
		return false
	}
}

func validQueuedSendFact(convID, typ string, raw json.RawMessage) bool {
	var p queuedSendFact
	if json.Unmarshal(raw, &p) != nil || p.ConversationID != convID {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for key, value := range fields {
		if strings.EqualFold(key, "queued_msg_id") {
			var id uint64
			if json.Unmarshal(value, &id) != nil || id == 0 || id != p.QueuedMessageID {
				return false
			}
		}
	}
	if typ == historySendAccepted {
		return !p.AcceptedAt.IsZero()
	}
	if p.OccurredAt.IsZero() || p.AcceptedEntryID == 0 {
		return false
	}
	switch typ {
	case historySendDelivered:
		return p.DeliveryEntryID != 0 && p.Reason == "delivered"
	case historySendDropped:
		return p.Reason == "removed" || p.Reason == "give_up"
	case historySendLost:
		return p.Reason == "daemon_restart"
	default:
		return false
	}
}

type queuedSendKey struct {
	convID  string
	queueID uint64
}
type queuedSendReference struct {
	ready chan struct{}
	id    *uint64
}

// References exist only for this daemon run. A placement may arrive before the
// acceptance callback, but cannot append or publish until that append completes.
type queuedSendHistory struct {
	store   *history.Store
	logger  *slog.Logger
	publish func(operatorMessage)
	source  func(conversations.ConversationID) *history.SessionProvenance
	mu      sync.Mutex
	refs    map[queuedSendKey]*queuedSendReference
}

func newQueuedSendHistory(store *history.Store, logger *slog.Logger, source func(conversations.ConversationID) *history.SessionProvenance) *queuedSendHistory {
	return &queuedSendHistory{store: store, logger: logger, source: source, refs: make(map[queuedSendKey]*queuedSendReference)}
}

func (h *queuedSendHistory) reference(c string, id uint64) *queuedSendReference {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := queuedSendKey{c, id}
	ref := h.refs[key]
	if ref == nil {
		ref = &queuedSendReference{ready: make(chan struct{})}
		h.refs[key] = ref
	}
	return ref
}

func (h *queuedSendHistory) observedSource(c string) history.SessionProvenance {
	if h.source != nil {
		if source := h.source(conversations.ConversationID(c)); source != nil {
			return *source
		}
	}
	return history.SessionProvenance{}
}

func (h *queuedSendHistory) append(typ string, p queuedSendFact, at time.Time, source history.SessionProvenance) *uint64 {
	raw, err := json.Marshal(p)
	if err != nil {
		h.logger.Warn("history: send fact marshal failed", "event", "send_history.marshal_err")
		return nil
	}
	return appendConversationHistory(h.store, h.logger, "send_history.append_err", p.ConversationID, typ, raw, at, source)
}

func (h *queuedSendHistory) accepted(c string, m msgqueue.QueuedMessage) {
	if m.ID == 0 {
		h.logger.Warn("history: invalid queue ID", "event", "send_history.invalid_queue_id")
		return
	}
	source := h.observedSource(c)
	ref := h.reference(c, m.ID)
	p := queuedSendFact{ConversationID: c, DeviceID: m.DeviceID, MessageID: m.MessageID, QueuedMessageID: m.ID, Text: m.Text, AttachmentIDs: m.AttachmentIDs, AcceptedAt: m.TS, ClientSentAt: formatClientSentAt(m.ClientSentAt)}
	ref.id = h.append(historySendAccepted, p, m.TS, source)
	h.notify(h.receipt(historySendAccepted, p, m.TS, ref.id))
	close(ref.ready)
}

func (h *queuedSendHistory) receipt(typ string, p queuedSendFact, at time.Time, id *uint64) *operatorMessage {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	payload, ok := legacyRuntimeReceipt(p.ConversationID, typ, raw, id, at)
	if !ok {
		return nil
	}
	return &operatorMessage{convID: p.ConversationID, typ: protocol.TypeBanner, payload: payload, ts: at, historyEntryID: id}
}

func (h *queuedSendHistory) notify(receipt *operatorMessage) {
	if receipt != nil && h.publish != nil {
		h.publish(*receipt)
	}
}

func (h *queuedSendHistory) beforeDelivery(c string, id uint64) *queuedSendReference {
	if h == nil {
		return nil
	}
	ref := h.reference(c, id)
	<-ref.ready
	return ref
}

func (h *queuedSendHistory) delivered(c string, id uint64, ref *queuedSendReference, deliveryID *uint64, at time.Time, source history.SessionProvenance) *operatorMessage {
	if h == nil {
		return nil
	}
	var receipt *operatorMessage
	if ref.id != nil && deliveryID != nil {
		p := queuedSendFact{ConversationID: c, AcceptedEntryID: *ref.id, DeliveryEntryID: *deliveryID, OccurredAt: at, Reason: string(msgqueue.TerminalDelivered)}
		entryID := h.append(historySendDelivered, p, at, source)
		receipt = h.receipt(historySendDelivered, p, at, entryID)
	}
	h.forget(c, id)
	return receipt
}

func (h *queuedSendHistory) forget(c string, id uint64) {
	h.mu.Lock()
	delete(h.refs, queuedSendKey{c, id})
	h.mu.Unlock()
}

// Delivery belongs to placement, including an echo before OnDelivered. The
// queue arbitrates delivery versus removal; only actual drops arrive here.
func (h *queuedSendHistory) terminal(c string, m msgqueue.QueuedMessage, outcome msgqueue.TerminalOutcome) {
	if outcome == msgqueue.TerminalDelivered {
		return
	}
	source := h.observedSource(c)
	ref := h.beforeDelivery(c, m.ID)
	if ref.id != nil {
		at := time.Now().UTC()
		p := queuedSendFact{ConversationID: c, AcceptedEntryID: *ref.id, OccurredAt: at, Reason: string(outcome)}
		entryID := h.append(historySendDropped, p, at, source)
		h.notify(h.receipt(historySendDropped, p, at, entryID))
	}
	h.forget(c, m.ID)
}

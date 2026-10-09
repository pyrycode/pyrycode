package main

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
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
	Text            string    `json:"text,omitempty"`
	AttachmentIDs   []string  `json:"attachment_ids,omitempty"`
	AcceptedAt      time.Time `json:"accepted_at,omitzero"`
	ClientSentAt    string    `json:"client_sent_at,omitempty"`
	AcceptedEntryID uint64    `json:"accepted_entry_id,omitempty"`
	DeliveryEntryID uint64    `json:"delivery_entry_id,omitempty"`
	OccurredAt      time.Time `json:"occurred_at,omitzero"`
	Reason          string    `json:"reason,omitempty"`
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
	store  *history.Store
	logger *slog.Logger
	source func(conversations.ConversationID) *history.SessionProvenance
	mu     sync.Mutex
	refs   map[queuedSendKey]*queuedSendReference
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
	source := h.observedSource(c)
	ref := h.reference(c, m.ID)
	ref.id = h.append(historySendAccepted, queuedSendFact{ConversationID: c, DeviceID: m.DeviceID, MessageID: m.MessageID, Text: m.Text, AttachmentIDs: m.AttachmentIDs, AcceptedAt: m.TS, ClientSentAt: formatClientSentAt(m.ClientSentAt)}, m.TS, source)
	close(ref.ready)
}

func (h *queuedSendHistory) beforeDelivery(c string, id uint64) *queuedSendReference {
	if h == nil {
		return nil
	}
	ref := h.reference(c, id)
	<-ref.ready
	return ref
}

func (h *queuedSendHistory) delivered(c string, id uint64, ref *queuedSendReference, deliveryID *uint64, at time.Time, source history.SessionProvenance) {
	if h == nil {
		return
	}
	if ref.id != nil && deliveryID != nil {
		h.append(historySendDelivered, queuedSendFact{ConversationID: c, AcceptedEntryID: *ref.id, DeliveryEntryID: *deliveryID, OccurredAt: at, Reason: string(msgqueue.TerminalDelivered)}, at, source)
	}
	h.forget(c, id)
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
		h.append(historySendDropped, queuedSendFact{ConversationID: c, AcceptedEntryID: *ref.id, OccurredAt: at, Reason: string(outcome)}, at, source)
	}
	h.forget(c, m.ID)
}

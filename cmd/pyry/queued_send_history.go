package main

import (
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/attachments"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
)

const (
	historySendAccepted  = "send_accepted"
	historySendWritten   = "send_written"
	historySendDelivered = "send_delivered"
	historySendDropped   = "send_dropped"
	historySendLost      = "send_lost"
)

type queuedSendFact struct {
	ConversationID  string                 `json:"conversation_id"`
	DeviceID        string                 `json:"device_id,omitempty"`
	MessageID       string                 `json:"message_id,omitempty"`
	QueuedMessageID uint64                 `json:"queued_msg_id,omitempty"`
	Text            string                 `json:"text,omitempty"`
	AttachmentIDs   []string               `json:"attachment_ids,omitempty"`
	Attachments     []queuedAttachmentFact `json:"attachments,omitempty"`
	AcceptedAt      time.Time              `json:"accepted_at,omitzero"`
	ClientSentAt    string                 `json:"client_sent_at,omitempty"`
	AcceptedEntryID uint64                 `json:"accepted_entry_id,omitempty"`
	DeliveryEntryID uint64                 `json:"delivery_entry_id,omitempty"`
	OccurredAt      time.Time              `json:"occurred_at,omitzero"`
	Reason          string                 `json:"reason,omitempty"`
}

type queuedAttachmentFact struct {
	AttachmentID string `json:"attachment_id"`
	Filename     string `json:"filename"`
}

func queuedSendHistoryType(typ string) bool {
	switch typ {
	case historySendAccepted, historySendWritten, historySendDelivered, historySendDropped, historySendLost:
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
	case historySendWritten:
		return p.DeliveryEntryID == 0 && p.Reason == "written"
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
	mu        sync.Mutex // Serializes only this acceptance's write/placement facts.
	written   bool
	completed bool
	ready     chan struct{}
	id        *uint64
}

// References exist only for this daemon run. A placement may arrive before the
// acceptance callback, but cannot append until that append completes.
type queuedSendHistory struct {
	store          *history.Store
	logger         *slog.Logger
	bindItem       func(string, uint64, uint64)
	source         func(conversations.ConversationID) *history.SessionProvenance
	attachmentName func(string, string) (string, bool)
	mu             sync.Mutex
	refs           map[queuedSendKey]*queuedSendReference
}

func newQueuedSendHistory(store *history.Store, logger *slog.Logger, source func(conversations.ConversationID) *history.SessionProvenance) *queuedSendHistory {
	return &queuedSendHistory{store: store, logger: logger, source: source, refs: make(map[queuedSendKey]*queuedSendReference)}
}

// storedQueuedAttachmentName resolves display names only within registered
// conversations. Lookup errors can contain host paths and never leave this seam.
func storedQueuedAttachmentName(instanceDir string, reg *conversations.Registry) func(string, string) (string, bool) {
	return func(c, id string) (string, bool) {
		convID := conversations.ConversationID(c)
		if _, ok := reg.Get(convID); !ok {
			return "", false
		}
		path, err := attachments.ResolvePath(instanceDir, convID, id)
		if err != nil {
			return "", false
		}
		return filepath.Base(path), true
	}
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
	if h.attachmentName != nil {
		seen := make(map[string]bool, len(m.AttachmentIDs))
		for _, id := range m.AttachmentIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			if name, ok := h.attachmentName(c, id); ok {
				p.Attachments = append(p.Attachments, queuedAttachmentFact{AttachmentID: id, Filename: name})
			}
		}
	}
	ref.id = h.append(historySendAccepted, p, m.TS, source)
	if ref.id != nil && h.bindItem != nil {
		h.bindItem(c, m.ID, *ref.id)
	}
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
	ref.mu.Lock()
	defer ref.mu.Unlock()
	if ref.completed {
		return
	}
	ref.completed = true
	if ref.id != nil && deliveryID != nil {
		p := queuedSendFact{ConversationID: c, AcceptedEntryID: *ref.id, DeliveryEntryID: *deliveryID, OccurredAt: at, Reason: string(msgqueue.TerminalDelivered)}
		h.append(historySendDelivered, p, at, source)
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
		if m.SentNow {
			h.written(c, m)
		}
		return
	}
	source := h.observedSource(c)
	ref := h.beforeDelivery(c, m.ID)
	if ref.id != nil {
		at := time.Now().UTC()
		p := queuedSendFact{ConversationID: c, AcceptedEntryID: *ref.id, OccurredAt: at, Reason: string(outcome)}
		h.append(historySendDropped, p, at, source)
	}
	h.forget(c, m.ID)
}

// written records physical stdin delivery, never consumption or final placement.
// No global coordinator lock spans I/O. The per-acceptance leaf lock keeps this
// optional intermediate fact ordered with its final placement and deduplicates it.
func (h *queuedSendHistory) written(c string, m msgqueue.QueuedMessage) {
	h.mu.Lock()
	ref := h.refs[queuedSendKey{c, m.ID}]
	h.mu.Unlock()
	if ref == nil {
		return
	} // A fast echo already completed and forgot the owner.
	<-ref.ready
	source := h.observedSource(c)
	ref.mu.Lock()
	defer ref.mu.Unlock()
	if ref.completed || ref.written || ref.id == nil {
		return
	}
	at := time.Now().UTC()
	p := queuedSendFact{ConversationID: c, AcceptedEntryID: *ref.id, OccurredAt: at, Reason: "written"}
	if h.append(historySendWritten, p, at, source) != nil {
		ref.written = true
	}
}

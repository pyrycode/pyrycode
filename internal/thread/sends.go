package thread

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
)

// sendRecord mirrors the safe producer fields, without importing daemon code.
// Scalar links distinguish malformed null from absent or unresolved zero.
type sendRecord struct {
	ConversationID  string    `json:"conversation_id"`
	DeviceID        string    `json:"device_id"`
	MessageID       string    `json:"message_id"`
	Text            string    `json:"text"`
	AttachmentIDs   []string  `json:"attachment_ids"`
	AcceptedAt      time.Time `json:"accepted_at"`
	ClientSentAt    string    `json:"client_sent_at"`
	AcceptedEntryID uint64    `json:"accepted_entry_id"`
	DeliveryEntryID uint64    `json:"delivery_entry_id"`
	OccurredAt      time.Time `json:"occurred_at"`
	Reason          string    `json:"reason"`
}

// sendFact consumes supported sends even when malformed, so they never enter
// standalone or main-work processing. Only valid earlier entries can be linked.
func (f *Fold) sendFact(e history.Entry) bool {
	status := ""
	switch e.Type {
	case "send_accepted":
		status = "queued"
	case "send_delivered":
		status = "delivered"
	case "send_dropped":
		status = "dropped"
	case "send_lost":
		status = "lost"
	default:
		return false
	}
	var p sendRecord
	if !decode(e.Payload, &p) {
		return true
	}
	validReason := (status == "queued" && p.Reason == "") ||
		(status == "delivered" && p.Reason == "delivered") ||
		(status == "dropped" && (p.Reason == "removed" || p.Reason == "give_up")) ||
		(status == "lost" && p.Reason == "daemon_restart")
	if !validReason {
		return true
	}
	if status == "queued" {
		f.accepted[e.ID] = f.addItem(e, Item{ID: e.ID, Rev: e.ID, Kind: "user_message", Status: status, Active: true, Shown: true, Summary: p.Text})
		return true
	}
	index, ok := f.accepted[p.AcceptedEntryID]
	if !ok || p.AcceptedEntryID >= e.ID || f.items[index].Status != "queued" {
		return true
	}
	item := &f.items[index]
	if status == "delivered" {
		delivery, ok := f.messages[p.DeliveryEntryID]
		if !ok || delivery < 0 || p.DeliveryEntryID >= e.ID {
			return true
		}
		message := f.items[delivery]
		item.Content = deliveredSendContent(item.Content, message.Content)
		item.Summary, item.Order = message.Summary, message.ID
		item.Session, item.Agent, item.NoChild = message.Session, message.Agent, message.NoChild
		// Preserve internal indexes held by main work; suppress only the public row.
		f.messages[p.DeliveryEntryID] = -1
	} else {
		item.Shown = status == "lost"
	}
	item.Status, item.Active, item.Rev = status, false, e.ID
	f.setContent(index, "outcome", e.Payload)
	return true
}

// deliveredSendContent uses safe receiving content while retaining recorded
// acceptance sender identity and times. These fields never establish a join.
func deliveredSendContent(accepted, delivered json.RawMessage) json.RawMessage {
	var sender struct {
		DeviceID     json.RawMessage `json:"device_id"`
		MessageID    json.RawMessage `json:"message_id"`
		AcceptedAt   json.RawMessage `json:"accepted_at"`
		ClientSentAt json.RawMessage `json:"client_sent_at"`
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(accepted, &sender) // Both objects were validated on ingestion.
	_ = json.Unmarshal(delivered, &fields)
	for key, value := range map[string]json.RawMessage{
		"device_id": sender.DeviceID, "message_id": sender.MessageID,
		"accepted_at": sender.AcceptedAt, "client_sent_at": sender.ClientSentAt,
	} {
		if value != nil {
			for name := range fields {
				if strings.EqualFold(name, key) {
					delete(fields, name)
				}
			}
			fields[key] = value
		}
	}
	raw, _ := json.Marshal(fields) // Inert fields contain already-valid JSON.
	return raw
}

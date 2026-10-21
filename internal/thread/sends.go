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
	QueuedMessageID uint64    `json:"queued_msg_id"`
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
func (f *Fold) sendFact(e history.Entry) (bool, error) {
	decode := f.decode
	status := ""
	switch e.Type {
	case "send_accepted":
		status = "queued"
	case "send_written":
		status = "written"
	case "send_delivered":
		status = "delivered"
	case "send_dropped":
		status = "dropped"
	case "send_lost":
		status = "lost"
	default:
		return false, nil
	}
	var p sendRecord
	if !decode(e.Payload, &p) {
		return true, nil
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(e.Payload, &fields) // decode validated the object.
	f.countIngestion(len(e.Payload))
	for key, value := range fields {
		f.countIngestion(len(key) + len("queued_msg_id"))
		if strings.EqualFold(key, "queued_msg_id") {
			var id uint64
			f.countIngestion(len(value))
			if json.Unmarshal(value, &id) != nil || id == 0 || id != p.QueuedMessageID {
				return true, nil
			}
		}
	}
	validReason := (status == "queued" && p.Reason == "") ||
		(status == "written" && p.Reason == "written" && p.DeliveryEntryID == 0) ||
		(status == "delivered" && p.Reason == "delivered") ||
		(status == "dropped" && (p.Reason == "removed" || p.Reason == "give_up")) ||
		(status == "lost" && p.Reason == "daemon_restart")
	if !validReason {
		return true, nil
	}
	if status == "queued" {
		f.accepted[e.ID] = f.addItem(e, Item{ID: e.ID, Rev: e.ID, Kind: "user_message", Status: status, Active: true, Shown: true, Summary: p.Text})
		return true, nil
	}
	index, ok := f.accepted[p.AcceptedEntryID]
	f.countPublicWork(1) // acceptance lookup, including rejected outcomes
	if !ok || p.AcceptedEntryID >= e.ID {
		return true, nil
	}
	prior := f.items[index].Status
	item := &f.items[index]
	// A linked message already proves delivery. Its first matching outcome may
	// enrich it once, without reopening the owner or claiming another message.
	earlyOutcome := prior == "delivered" && status == "delivered" &&
		item.Order == p.DeliveryEntryID && item.Rev == item.Order && f.messages[p.DeliveryEntryID] < 0
	if !earlyOutcome && prior != "queued" && !(prior == "written" && (status == "delivered" || status == "lost")) {
		return true, nil
	}
	f.countPublicWork(1)
	if status == "delivered" && !earlyOutcome {
		delivery, ok := f.messages[p.DeliveryEntryID]
		f.countPublicWork(1)
		if !ok || delivery < 0 || p.DeliveryEntryID >= e.ID {
			return true, nil
		}
		if err := f.deliverSend(index, delivery, true); err != nil {
			return true, err
		}
		if f.messages[p.DeliveryEntryID] >= 0 {
			return true, nil // Explicit references cannot enter legacy joins.
		}
	} else if status != "delivered" {
		item.Shown = status == "lost" || status == "written"
	}
	item.Status, item.Active, item.Rev = status, status == "written", e.ID
	if f.recipes[item.ID].creating != 0 {
		f.referenceField(index, "outcome", recipeField{entry: e.ID})
	} else {
		f.setContent(index, "outcome", e.Payload)
	}
	return true, nil
}

// deliveryReference distinguishes absent legacy links from invalid explicit ones.
// Every case variant must name the same strictly earlier durable identity.
func (f *Fold) deliveryReference(e history.Entry) (uint64, bool) {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(e.Payload, &fields) // The message was validated on ingestion.
	f.countIngestion(len(e.Payload))
	var accepted uint64
	present := false
	for key, raw := range fields {
		f.countIngestion(len(key) + len("accepted_entry_id"))
		if !strings.EqualFold(key, "accepted_entry_id") {
			continue
		}
		present = true
		var id uint64
		f.countIngestion(len(raw))
		if json.Unmarshal(raw, &id) != nil || id == 0 || id >= e.ID || (accepted != 0 && accepted != id) {
			return 0, true
		}
		accepted = id
	}
	return accepted, present
}

func (f *Fold) sendMessage(e history.Entry, delivery int) error {
	id, _ := f.deliveryReference(e)
	index, ok := f.accepted[id]
	f.countPublicWork(1)
	if !ok || (f.items[index].Status != "queued" && f.items[index].Status != "written") {
		return nil
	}
	if err := f.deliverSend(index, delivery, false); err != nil {
		return err
	}
	item := &f.items[index]
	item.Status, item.Active, item.Rev = "delivered", false, e.ID
	return nil
}

func (f *Fold) deliverSend(index, delivery int, legacy bool) error {
	item := &f.items[index]
	// Validate both sources before claiming or suppressing either identity.
	rendered, err := f.render([]Item{*item, f.items[delivery]})
	if err != nil {
		return err
	}
	message := rendered[1]
	if legacy {
		if _, explicit := f.deliveryReference(history.Entry{ID: message.ID, Payload: message.Content}); explicit {
			return nil
		}
	}
	f.countPublicWork(1)
	if r := f.recipes[item.ID]; r.creating != 0 {
		r.delivery = message.ID
		f.recipes[item.ID] = r
	} else {
		f.countIngestion(len(item.Content) + len(message.Content))
		item.Content = deliveredSendContent(item.Content, message.Content)
		f.countIngestion(len(item.Content) * 3)
		item.Summary = message.Summary
	}
	item.Order = message.ID
	item.Session, item.Agent, item.NoChild = message.Session, message.Agent, message.NoChild
	// Preserve indexes held by main work; suppress only the public row, before
	// a newly linked message has ever appeared in a captured publication.
	f.messages[message.ID] = -1
	f.markAffected(delivery)
	f.markAffected(index)
	return nil
}

// deliveredSendContent uses safe receiving content while retaining recorded
// acceptance sender identity, times and queue handle. A terminal handle is
// historical data; only a queued active item supplies a queue action target.
// These fields never establish a history join.
func deliveredSendContent(accepted, delivered json.RawMessage) json.RawMessage {
	var sender struct {
		DeviceID        json.RawMessage `json:"device_id"`
		MessageID       json.RawMessage `json:"message_id"`
		AcceptedAt      json.RawMessage `json:"accepted_at"`
		ClientSentAt    json.RawMessage `json:"client_sent_at"`
		QueuedMessageID json.RawMessage `json:"queued_msg_id"`
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(accepted, &sender) // Both objects were validated on ingestion.
	_ = json.Unmarshal(delivered, &fields)
	// A legacy acceptance cannot acquire a handle from its delivered message.
	for key := range fields {
		if strings.EqualFold(key, "queued_msg_id") {
			delete(fields, key)
		}
	}
	for key, value := range map[string]json.RawMessage{
		"device_id": sender.DeviceID, "message_id": sender.MessageID,
		"accepted_at": sender.AcceptedAt, "client_sent_at": sender.ClientSentAt,
		"queued_msg_id": sender.QueuedMessageID,
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

//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func hookBannerIsRefusal(p protocol.BannerPayload, convID string) bool {
	return p.ConversationID == convID && bytes.Contains([]byte(p.Text), []byte(oslcapBlockReason))
}

// hookBannerCheckWindow joins legacy banners to raw facts, whose durable IDs
// distinguish otherwise identical receipts. Queued receipts have no ack gate.
func hookBannerCheckWindow(convID string, liveReqID uint64, entries []history.Entry, frames []protocol.Envelope) error {
	raw := make(map[uint64]history.Entry, len(entries))
	for _, entry := range entries {
		raw[entry.ID] = entry
	}
	positions := make(map[string]int)
	receiptIDs := make(map[string]uint64)
	seenIDs, seenEvents := make(map[uint64]bool), make(map[uint64]bool)
	ack, firstDelta, opening := -1, -1, -1
	var openingTurn, assistantTurn string
	var refusalEntry, openingEntry history.Entry
	for i, env := range frames {
		switch env.Type {
		case protocol.TypeError, protocol.TypeUnrecognizedMessage:
			return fmt.Errorf("unexpected envelope type %q", env.Type)
		case protocol.TypeAck:
			if env.InReplyTo != nil && *env.InReplyTo == liveReqID {
				ack = i
			}
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				return fmt.Errorf("decode assistant delta: %w", err)
			}
			if p.ConversationID == convID && p.Text != "" {
				if ack < 0 {
					return fmt.Errorf("assistant text before liveness ack")
				}
				if firstDelta < 0 {
					firstDelta, assistantTurn = i, p.TurnID
				}
			}
		case protocol.TypeTurnState, protocol.TypeTurnEnd:
			var p struct {
				ConversationID string `json:"conversation_id"`
			}
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				return fmt.Errorf("decode lifecycle frame: %w", err)
			}
			if p.ConversationID == convID && ack < 0 {
				return fmt.Errorf("refused prompt emitted %q before liveness ack", env.Type)
			}
		case protocol.TypeBanner:
			if env.HistoryEntryID == nil || *env.HistoryEntryID == 0 || env.EventID == nil || *env.EventID == 0 || env.TS.IsZero() {
				return fmt.Errorf("banner lacks durable/replay identity or timestamp")
			}
			id := *env.HistoryEntryID
			entry, ok := raw[id]
			if !ok || !entry.TS.Equal(env.TS) || seenIDs[id] || seenEvents[*env.EventID] {
				return fmt.Errorf("banner identity %d missing, duplicated or timestamp mismatched", id)
			}
			seenIDs[id], seenEvents[*env.EventID] = true, true
			var p protocol.BannerPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				return fmt.Errorf("decode banner: %w", err)
			}
			key := "refusal"
			if entry.Type == protocol.TypeBanner {
				var durable protocol.BannerPayload
				if err := json.Unmarshal(entry.Payload, &durable); err != nil || durable != p || !hookBannerIsRefusal(p, convID) || p.Truncated {
					return fmt.Errorf("durable banner %d is not the intact hook refusal", id)
				}
				refusalEntry = entry
			} else {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(env.Payload, &fields); err != nil {
					return fmt.Errorf("decode receipt fields: %w", err)
				}
				for _, field := range []string{"conversation_id", "level", "text", "truncated", "stops_turn"} {
					value, exists := fields[field]
					if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
						return fmt.Errorf("receipt %d lacks nonnull %q", id, field)
					}
				}
				if p != (protocol.BannerPayload{ConversationID: convID, Level: "info"}) {
					return fmt.Errorf("receipt %d is not an empty info banner", id)
				}
				var fact struct {
					ConversationID  string    `json:"conversation_id"`
					MessageID       string    `json:"message_id"`
					AcceptedAt      time.Time `json:"accepted_at"`
					AcceptedEntryID uint64    `json:"accepted_entry_id"`
					DeliveryEntryID uint64    `json:"delivery_entry_id"`
					OccurredAt      time.Time `json:"occurred_at"`
					Reason          string    `json:"reason"`
					TurnID          string    `json:"turn_id"`
				}
				if err := json.Unmarshal(entry.Payload, &fact); err != nil || fact.ConversationID != convID {
					return fmt.Errorf("receipt %d has invalid raw fact", id)
				}
				switch entry.Type {
				case "send_accepted":
					if !fact.AcceptedAt.Equal(entry.TS) {
						return fmt.Errorf("acceptance %d timestamp mismatch", id)
					}
					key = "accepted:" + fact.MessageID
				case "send_delivered":
					accepted, delivery := raw[fact.AcceptedEntryID], raw[fact.DeliveryEntryID]
					var a struct {
						ConversationID string `json:"conversation_id"`
						MessageID      string `json:"message_id"`
					}
					var message protocol.MessagePayload
					if json.Unmarshal(accepted.Payload, &a) != nil || json.Unmarshal(delivery.Payload, &message) != nil ||
						accepted.Type != "send_accepted" || delivery.Type != protocol.TypeMessage ||
						a.ConversationID != convID || message.ConversationID != convID || message.Role != "user" || a.MessageID != message.MessageID ||
						accepted.ID >= delivery.ID || delivery.ID >= id || fact.Reason != "delivered" || !fact.OccurredAt.Equal(entry.TS) {
						return fmt.Errorf("delivery %d has invalid acceptance/user-message link", id)
					}
					key = "delivered:" + a.MessageID
					acceptedPosition, exists := positions["accepted:"+a.MessageID]
					if !exists || acceptedPosition >= i || receiptIDs["accepted:"+a.MessageID] != accepted.ID {
						return fmt.Errorf("delivery %d does not follow its identified acceptance receipt", id)
					}
				case "main_turn_opened":
					if fact.TurnID == "" || !fact.OccurredAt.Equal(entry.TS) || ack < 0 || firstDelta >= 0 {
						return fmt.Errorf("opening %d must follow ack and precede assistant text", id)
					}
					key, opening, openingTurn, openingEntry = "opening", i, fact.TurnID, entry
				default:
					return fmt.Errorf("unexpected receipt fact %q at %d", entry.Type, id)
				}
			}
			if _, exists := positions[key]; exists {
				return fmt.Errorf("duplicate banner for %q", key)
			}
			positions[key] = i
			receiptIDs[key] = id
		}
	}
	for _, key := range []string{"refusal", "accepted:m-blocked", "delivered:m-blocked", "accepted:m-live", "delivered:m-live", "opening"} {
		if _, exists := positions[key]; !exists {
			return fmt.Errorf("missing banner for %q", key)
		}
	}
	if len(positions) != 6 {
		return fmt.Errorf("unexpected banners: got %d, want 6", len(positions))
	}
	if ack < 0 || firstDelta <= opening || openingTurn != assistantTurn || openingEntry.ID <= refusalEntry.ID || openingEntry.TS.Before(refusalEntry.TS) {
		return fmt.Errorf("opening does not identify the post-refusal assistant turn")
	}
	return nil
}

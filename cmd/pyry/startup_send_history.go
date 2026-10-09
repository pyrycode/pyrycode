package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

// Read the whole raw log before inferring loss. Queue IDs and current registry
// bindings have no role in resolving a surviving durable acceptance.
func readStartupSends(store *history.Store, c conversations.ConversationID) ([]history.Entry, error) {
	resolved := make(map[uint64]bool)
	var accepted []history.Entry
	var cursor string
	for {
		page, err := store.Page(c, cursor, 128)
		if err != nil {
			return nil, err
		}
		for _, e := range page.Entries {
			switch e.Type {
			case historySendAccepted, historySendDelivered, historySendDropped, historySendLost:
			default:
				continue
			}
			var p queuedSendFact
			if json.Unmarshal(e.Payload, &p) != nil || p.ConversationID != string(c) || (e.Type != historySendAccepted && p.AcceptedEntryID == 0) {
				return nil, errors.New("invalid send history fact")
			}
			if e.Type == historySendAccepted {
				e.Payload = nil
				accepted = append(accepted, e)
			} else {
				resolved[p.AcceptedEntryID] = true
			}
		}
		if page.AtStart {
			break
		}
		cursor = page.Cursor
	}
	accepted = slices.DeleteFunc(accepted, func(e history.Entry) bool { return resolved[e.ID] })
	slices.Reverse(accepted)
	return accepted, nil
}

func closeStartupSends(store *history.Store, logger *slog.Logger, c string, accepted []history.Entry, at time.Time) {
	h := newQueuedSendHistory(store, logger, nil)
	for _, e := range accepted {
		var source history.SessionProvenance
		if e.Session != nil {
			source = *e.Session
		}
		h.append(historySendLost, queuedSendFact{ConversationID: c, AcceptedEntryID: e.ID, OccurredAt: at, Reason: "daemon_restart"}, at, source)
	}
}

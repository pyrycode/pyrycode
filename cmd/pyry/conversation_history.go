package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// appendConversationHistory records one marshalled event with explicit visibility.
// The concrete store pointer avoids typed-nil interfaces. Nil or failed storage
// returns nil identity; eligible legacy events still publish and enter replay.
// Failure logs carry only the event, canonical conversation ID and a content-free
// reason, because payloads and filesystem paths must never reach telemetry.
// An optional producing source is combined with visibility; omission preserves
// absent provenance rather than inferring the current session or inventing none.
func appendConversationHistory(store *history.Store, logger *slog.Logger, event, convID, typ string, payload json.RawMessage, ts time.Time, source ...history.SessionProvenance) *uint64 {
	if store == nil {
		return nil
	}
	metadata := historyVisibilityMetadata(typ, payload)
	if len(source) > 0 && source[0].Kind != "" {
		captured := source[0]
		metadata.Session = &captured
	}
	id, err := store.AppendWithMetadata(conversations.ConversationID(convID), typ, payload, ts, metadata)
	if err != nil {
		// Warn, not Debug: the neighbouring per-conn drops lose one frame to one
		// conn, while this loses an event from the durable record permanently —
		// the same class as session_transition.queue_full, which is already Warn.
		// None of the reasons is reachable in a healthy daemon: ids come from the
		// registry and are canonical, payloads are closed structs, and the write
		// path is a local file under the instance directory.
		logger.Warn("relay: conversation-history append dropped",
			"event", event,
			"conversation_id", convID,
			"reason", historyAppendFailure(err))
		return nil
	}
	return &id
}

// newHistoryPager serves the legacy projection of exactly one bounded raw page.
// It preserves the opaque cursor and AtStart even when filtering removes every
// entry: consumers terminate on AtStart, not on an empty entry list.
//
// A nil store returns HistoryPageUnavailable; unlike a nil pager seam, this is
// an active verb with a retryable failure. Store validates cursors unchanged.
// Invalid cursors are client faults; other errors become generic unavailable
// outcomes. Only content-free discriminants reach logs, never errors containing
// filesystem paths, payloads or cursors.
func newHistoryPager(store *history.Store, logger *slog.Logger) relay.HistoryPager {
	return func(convID, cursor string, limit int) relay.HistoryPageResult {
		if store == nil {
			return relay.HistoryPageResult{Outcome: relay.HistoryPageUnavailable}
		}
		// cursor is passed through UNPARSED: history.parseCursor is the only
		// validator anywhere and nothing above internal/history may decode one.
		page, err := store.Page(conversations.ConversationID(convID), cursor, limit)
		if err != nil {
			if errors.Is(err, history.ErrInvalidCursor) {
				return relay.HistoryPageResult{Outcome: relay.HistoryPageBadCursor}
			}
			// The conversation id is canonical here — the relay's membership gate
			// established that before calling — so logging it is the shape-validated
			// case § Conversation history permits. The cursor is NOT logged on any
			// arm and is not in scope for this line.
			logger.Warn("relay: conversation-history page failed",
				"event", "history_page.read_err",
				"conversation_id", convID,
				"reason", historyPageFailure(err))
			return relay.HistoryPageResult{Outcome: relay.HistoryPageUnavailable}
		}
		// SIZED FROM WHAT THE LOG RETURNED, NEVER FROM limit. make(..., 0, limit)
		// reads as ordinary Go and would pin capacity chosen by a remote caller —
		// the rule protocol.RequestHistoryPayload.Limit states in as many words.
		entries := make([]protocol.HistoryEntry, 0, len(page.Entries))
		for _, e := range page.Entries {
			typ, payload := e.Type, e.Payload
			if !legacyHistoryType(typ) {
				var ok bool
				payload, ok = legacyRuntimeReceipt(convID, typ, payload, &e.ID, e.TS)
				if !ok {
					continue
				}
				typ = protocol.TypeBanner
			}
			entries = append(entries, protocol.HistoryEntry{
				ID: e.ID, Type: typ, Payload: payload, TS: e.TS,
			})
		}
		return relay.HistoryPageResult{
			Entries: entries,
			Cursor:  page.Cursor,
			AtStart: page.AtStart,
		}
	}
}

// historyPageFailure maps a Page error onto a content-free discriminant, the
// read-path twin of historyAppendFailure and for the same reason: the identity is
// logged rather than the error because internal/history's messages format
// absolute filesystem paths.
//
// It names MORE arms than the append path does, because a read can fail in ways a
// write cannot. ErrNotContained earns its own arm despite being unreachable past
// the membership gate: it is the symlink-containment refusal, so an occurrence is
// an attack signal rather than a malfunction, and an operator must be able to tell
// it from an ordinary I/O failure even while the client sees the same merged code.
func historyPageFailure(err error) string {
	switch {
	case errors.Is(err, history.ErrInvalidCursor):
		return "invalid_cursor"
	case errors.Is(err, history.ErrInvalidID):
		return "invalid_id"
	case errors.Is(err, history.ErrInvalidPageSize):
		return "invalid_page_size"
	case errors.Is(err, history.ErrNotContained):
		return "not_contained"
	case errors.Is(err, history.ErrCorruptSegment):
		return "corrupt_segment"
	case errors.Is(err, history.ErrUnknownVersion):
		return "unknown_version"
	default:
		return "read"
	}
}

// historyAppendFailure maps an Append error onto a content-free discriminant.
//
// The identity is logged rather than the error itself because
// internal/history's errors, while free of payload bytes by construction
// (encodeEntry and decodeSegment both say so explicitly), DO format absolute
// filesystem paths — "open segment %q", "resolve log directory %q". A
// discriminant derived by errors.Is carries which refusal happened without the
// path, and stays immune if a future change to that package ever puts something
// new in a message.
func historyAppendFailure(err error) string {
	switch {
	case errors.Is(err, history.ErrInvalidID):
		return "invalid_id"
	case errors.Is(err, history.ErrInvalidPayload):
		return "invalid_payload"
	default:
		return "write"
	}
}

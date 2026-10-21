package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
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

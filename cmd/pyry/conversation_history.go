package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

// appendConversationHistory records one already-marshalled envelope in convID's
// durable log (#2112's Store, of which #2114 is the first production caller).
// It is the single seam both v2 stream producers use — the interactive
// chokepoint's emit and the session-transition broadcast — so the
// never-log-content discipline below is written once rather than copied into two
// files where the two copies could drift.
//
// store may be nil, and a nil store is a no-op that logs nothing: a daemon wired
// without a log, and every emitter test that constructs an emitter without one,
// emit exactly as they did before this seam existed. *history.Store is
// CONCRETE, never an interface — the trap startSessionTransitionStreamV2 already
// records for busy: a typed-nil inside an interface is non-nil at the interface
// level and would route straight past a guard like this one. Store is not
// nil-receiver-safe either (Append takes its mutex immediately), which is why the
// guard lives here at the one call site rather than inside a method.
//
// A failure NEVER suppresses the caller's wire emit: this function returns
// nothing, so there is no branch for a caller to take. The minted id is
// discarded — it surfaces as history.Entry.ID in a served page (#2116), and the
// turn payloads already carry turn_id and seq, which storing the payload bytes
// verbatim retains.
//
// SECURITY: payload is conversation content and the log is the one place it may
// be written, so the failure line carries only event, conversation_id and a
// content-free reason. It deliberately does NOT carry err — see
// historyAppendFailure.
func appendConversationHistory(store *history.Store, logger *slog.Logger, event, convID, typ string, payload json.RawMessage, ts time.Time) {
	if store == nil {
		return
	}
	if _, err := store.Append(conversations.ConversationID(convID), typ, payload, ts); err != nil {
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

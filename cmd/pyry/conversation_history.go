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

// newHistoryPager adapts *history.Store to the relay's HistoryPage seam (#2116):
// the READ half of the append seam above, over the SAME store, so a served page
// and the entries this process just appended cannot come from two stores that
// mint duplicate ids for one conversation.
//
// store may be nil, and a nil store answers HistoryPageUnavailable rather than
// leaving the seam itself nil. The distinction is deliberate: a NIL SEAM makes
// the verb inert — the frame is consumed and not one byte of it is parsed — which
// is the posture for a daemon build with no history at all, while a wired seam
// over an absent store is a daemon that HAS the verb and cannot answer right now.
// The second is retryable and the first is silence, and conflating them would
// either parse remote bytes on a daemon that has no log or answer silence to a
// client whose daemon merely failed to open one.
//
// SENTINELS ARE CLASSIFIED HERE, NOT ABOVE, which is the whole reason this
// adapter exists: internal/relay imports internal/history nowhere, and the
// outcome discriminant is the only thing that crosses. errors.Is rather than
// string matching, the shape historyAppendFailure already uses for the write path.
//
// ErrInvalidCursor IS THE ONE CLIENT FAULT and it already carries all three of the
// merged causes — history.parseCursor raises it for a cursor that does not decode
// and for one minted for another conversation, and Store.Page's own cursorRefusal
// raises it for one naming a segment not in this log — so the merge the wire
// requires is structural rather than assembled here.
//
// ErrInvalidID and ErrInvalidPageSize fall to the default arm and are UNREACHABLE
// BY CONSTRUCTION: the handler's KnownConversation gate fires before this is
// called and the registry holds canonical ids only, and the handler never passes
// a limit below one. Answering them as a daemon problem rather than as the
// client's malformed request is the fail-safe direction — a daemon bug must not
// be reported to a client as its own fault.
//
// SECURITY: the error NEVER crosses the seam, because internal/history's messages
// format absolute filesystem paths ("open segment %q", "resolve log directory
// %q"). It is not silently discarded either — unlike attachmentResolve's, whose
// message carries a raw client string and therefore cannot be logged at all. Here
// the discriminant is content-free, and an operator has to be able to see that a
// segment has gone corrupt while a client must not.
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
			// Key for key, and the PAYLOAD BYTES ARE COPIED BY REFERENCE, UNCHANGED:
			// not re-decoded, not re-encoded, not sanitised. That is what makes a
			// served page reducible through the client's existing live-lane reducer,
			// and § Security model's threat 1 lands on the client exactly as it does
			// on the live lane.
			entries = append(entries, protocol.HistoryEntry{
				ID:      e.ID,
				Type:    e.Type,
				Payload: e.Payload,
				TS:      e.TS,
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

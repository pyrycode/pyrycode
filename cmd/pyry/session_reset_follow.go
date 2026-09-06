package main

import (
	"errors"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// sessionResetFollower follows claude's own announcement that it reset the
// conversation (#2135): it moves the runner's live session tag onto the announced
// id and re-keys the pool session behind it, so the client sees one session
// delimiter and the still-running child's later turn events keep reaching it.
//
// # Why it sits where it sits
//
// It is a sink DECORATOR on the parser's side of the fan-in channel, chained by
// newStreamRunnerFactory between the four retention holds and sink.sinkForTag.
// Two constraints bind at once and only that placement satisfies both:
//
//   - NOT behind the drain's active-session gate, which drops every event whose
//     producing session is not the ACTIVE conversation's bound session. A reset
//     observed there would never rotate a BACKGROUND conversation — the common
//     case, since an operator resetting one conversation is not thereby looking
//     at it.
//   - UPSTREAM of the fan-in send. turnevent.ConversationReset is droppable class
//     (turnMarkFor's default arm answers turnMarkNone, which
//     TestTurnMarkFor_TotalOverEveryVariant pins), so sinkForTag refuses it at
//     droppableCap under load. Anything that reads the event back OUT of the
//     channel loses the reset exactly when the daemon is busiest, which is when a
//     dark conversation costs the most.
//
// # Why it is not a fifth sessionRetentions member
//
// It retains nothing and exposes no accessor: it is an ACTION on two per-runner
// objects. Minting it inside newSessionParser would force that function to take
// the runner's live tag and the pool callback — runner-level dependencies it has
// no business knowing — and spend the five call sites #2106 paid to make a fifth
// hold free. The chain's own doc already supplies the licence for chaining it at
// the tail instead: what puts a link upstream of the fan-in send is sitting on the
// parser's side of the channel, not where in the chain it sits.
//
// SECURITY: this is a trust boundary the daemon did not have before. Until #2135
// a line on the supervised child's stdout could at most produce a client-visible
// event; now one line mutates the session registry. The value's SHAPE is settled
// upstream — streamsup's emitConversationReset gates on transcript.ValidStem, an
// anchored full match over lowercase hex, so nothing carrying a separator, a
// traversal or a length ever reaches here and this type re-derives none of it. Its
// DESTINATION is settled downstream, by Pool.AdoptAnnouncedID's collision refusal.
// This type owns neither question; it owns the ORDER, below.
type sessionResetFollower struct {
	// tag is the runner's live session tag — both the source of the id being
	// replaced and the thing replaced. Never a construction-time id: a captured one
	// would be right for exactly one reset and wrong for every later one.
	tag *streamSessionTag
	// adopt re-keys the pool session, from sessions.RunnerConfig.AdoptAnnouncedReset.
	// nil calls nothing — the tag still rotates, which is the half this type owns.
	adopt func(oldID, newID string) error
	// next is the downstream sink every event is forwarded to, unchanged. nil
	// forwards nothing — a test convenience; production always supplies
	// streamTurnSink.sinkForTag's closure.
	next   func(turnevent.Event)
	logger *slog.Logger
}

// newSessionResetFollower mints a follower that forwards to next. A nil logger
// falls back to slog.Default, matching newStreamTurnSink.
func newSessionResetFollower(tag *streamSessionTag, adopt func(oldID, newID string) error,
	next func(turnevent.Event), logger *slog.Logger) *sessionResetFollower {
	if logger == nil {
		logger = slog.Default()
	}
	return &sessionResetFollower{tag: tag, adopt: adopt, next: next, logger: logger}
}

// Sink is the decorator, used as a method value: follower.Sink is a
// func(turnevent.Event) of exactly the shape streamsup.NewParser takes.
//
// It acts on ConversationReset and then forwards EVERY event of EVERY variant
// unchanged, ConversationReset included — the contract each retention hold's Sink
// states, for the same reason: swallowing one here would change what the fan-in
// and the drain observe, which this ticket has no reason to do. The forwarded
// reset therefore carries the NEW tag, because sinkForTag reads the tag after this
// returns. Immaterial either way — turnMarkFor answers turnMarkNone for the
// variant so turnBusyTracker.observe is a no-op on it, and
// interactiveTurnEmitterV2.Handle has no arm for it — but stated so a reader of a
// drop record knows which id to expect beside it.
func (f *sessionResetFollower) Sink(ev turnevent.Event) {
	if reset, ok := ev.(turnevent.ConversationReset); ok {
		f.follow(reset.NewConversationID)
	}
	if f.next != nil {
		f.next(ev)
	}
}

// follow is the whole of the ordering contract, and every step's position is
// load-bearing.
//
// The id being replaced is read from the LIVE tag, so a second announced reset in
// the same session rotates from the first one's id rather than from the runner's
// construction-time one.
//
// An announcement of the id the session already has returns having touched
// NOTHING: no tag rotation, no pool call, and therefore no delimiter. The tag
// guard can only live here — the pool cannot guard a value it does not own — and
// Pool.AdoptAnnouncedID guards the re-key half at the seam that owns THAT. The two
// are not duplicates of each other.
//
// The tag rotates BEFORE the pool call and UNCONDITIONALLY on the differing-id
// path. Unconditionally because the pool's rotation watcher may have observed the
// same rotation first (it fires on the new transcript's creation), in which case
// the pool call returns ErrSessionNotFound while the conversation is ALREADY bound
// to the announced id — and a tag rotation made conditional on the pool's success
// would then leave every later event tagged with an id the drain's active-session
// gate no longer admits. That is the dark conversation this ticket exists to
// prevent, and it is worse than the noise row it would replace. Before, because
// the pool call rebinds the owning conversation, and the rebind is what makes the
// old tag dead: rotating first means no other goroutine can observe the rebind
// while the tag still reports the id it retired.
//
// The whole sequence runs SYNCHRONOUSLY on claude's stdout forwarder goroutine —
// the goroutine that also produces every later event of this session — so no event
// can be produced between the two steps. An asynchronous hand-off would reopen
// exactly that window; it was rejected for that, not for cost.
func (f *sessionResetFollower) follow(newID string) {
	oldID := f.tag.ID()
	if newID == oldID {
		return
	}
	f.tag.Rotate(newID)
	if f.adopt == nil {
		return
	}
	err := f.adopt(oldID, newID)
	if err == nil {
		return
	}
	// SECURITY: content-free — the two session ids and the error, never anything
	// claude authored beyond an id already validated as a UUID stem upstream. The
	// event itself is never logged, and no decode error can reach here: streamsup's
	// emitConversationReset deliberately has no logging surface at all, because
	// encoding/json quotes the offending input into its error text.
	//
	// ErrSessionNotFound is the EXPECTED outcome whenever the rotation watcher won
	// the race to the same rotation, which is every reset until #2137 retires it —
	// so it is Debug. Warn there would cry wolf on the normal path and train a
	// reader to skip the record that matters.
	if errors.Is(err, sessions.ErrSessionNotFound) {
		f.logger.Debug("relay: announced reset already applied",
			"event", "announced_reset.already_applied",
			"session_id", newID,
			"previous_session_id", oldID,
			"err", err)
		return
	}
	f.logger.Warn("relay: announced reset re-key failed",
		"event", "announced_reset.rekey_failed",
		"session_id", newID,
		"previous_session_id", oldID,
		"err", err)
}

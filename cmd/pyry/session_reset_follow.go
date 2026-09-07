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
// DESTINATION is decided downstream, by Pool.AdoptAnnouncedID's collision refusal —
// but a refusal only BINDS if this type honours it, because the pool cannot reach
// the tag. An announced id naming another live session that left the tag rotated
// would stamp every later event of THIS runner with the other session's id, which
// hands the child's output to a conversation it does not belong to. follow honours
// the refusal by unwinding, and that is this type's half of the boundary: it owns
// neither the shape nor the destination, it owns the ORDER and the UNWIND, below.
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
// The tag rotates BEFORE the pool call, and is UNWOUND afterwards unless the
// pool's answer means the session is now on the announced id. The rule stated
// positively, because it is the rule and not the two sentinels that must survive a
// new one being added: the tag ends on newID exactly when adopt answers nil (this
// call re-keyed) or ErrSessionNotFound (someone else already did); every other
// answer means NOTHING moved, and the tag goes back.
//
// Rotating first, rather than after a successful call: the pool call rebinds the
// owning conversation, and the rebind is what makes the old tag dead. Doing it
// first means no other goroutine can ever observe the rebind while the tag still
// reports the id it retired — a window that would otherwise open on EVERY
// successful reset, spanning a registry write and the observer fan-out.
//
// Rotating unconditionally, and only then unwinding, rather than waiting for the
// answer: the watcher may have observed the same rotation first (it fires on the
// new transcript's creation), so ErrSessionNotFound is the answer on a path where
// the conversation is ALREADY bound to the announced id. A tag left behind there
// would have every later event dropped by the drain's active-session gate — the
// dark conversation this ticket exists to prevent, and worse than the noise row it
// would replace.
//
// The UNWIND is what ErrSessionIDTaken needs, and it is not the same case wearing
// a different sentinel. AdoptAnnouncedID returns it BEFORE rekeyLocked: nothing was
// re-keyed and no conversation was rebound, and the announced id still belongs to a
// DIFFERENT live session. A tag left on it would stamp this runner's later events
// with the other session's id — sinkForTag reads the tag once per event — so the
// drain's gate would ADMIT them into that conversation whenever the cursor sits
// there, while this runner's own conversation went dark and exitForTag reported
// this child's exit as the other session's. One line on the supervised child's
// stdout would have moved a conversation's output to another conversation. The
// announcement is ignored instead: tag unrotated, registry unchanged, one Warn.
//
// The unwind leaves one window and it is bounded and strictly smaller than the one
// it replaces. Between the rotation and the unwind the tag reads newID, and
// exitForTag runs on the runner's supervision goroutine rather than this one, so a
// child exiting exactly there would be reported under the announced id. That window
// spans only the refusal path, which returns before any disk write — a lock
// acquisition and two map probes. Rotating after the call instead would trade it
// for a window on every SUCCESSFUL reset, spanning the registry save and the
// observer fan-out, and that is the common path.
//
// Everything else runs SYNCHRONOUSLY on claude's stdout forwarder goroutine — the
// goroutine that also produces every later event of this session — so no event can
// be produced between the steps. An asynchronous hand-off would reopen exactly that
// window; it was rejected for that, not for cost.
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
	// Anything else means the pool moved nothing — ErrSessionIDTaken today, and any
	// sentinel added later — so the tag goes back before the record is written. Warn
	// rather than Debug: a refusal is claude naming an id the daemon cannot follow it
	// to, which is either a defect or a confused child, and it is rare enough that it
	// cannot bury the record above.
	f.tag.Rotate(oldID)
	f.logger.Warn("relay: announced reset refused, tag left on the previous id",
		"event", "announced_reset.refused",
		"session_id", newID,
		"previous_session_id", oldID,
		"err", err)
}

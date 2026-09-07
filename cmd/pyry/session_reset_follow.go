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
//
// #2176 adds the third thing it owns, for the same reason and at the same boundary:
// both of its writes to the tag are CONDITIONAL on the tag still holding the value
// this call read. The tag has a second writer, on another goroutine, that drove its
// own rotation and re-keyed the registry to match; a follower that stored over it
// would leave the tag and the registry naming different sessions in either direction.
// A refusal only binds if the caller unwinds to match it, and an unwind only binds if
// it does not overwrite a winner. See follow.
//
// #2136 extends how far the announced value reaches without moving that boundary.
// The id now also becomes the runner's live spawn id, and beginSpawn puts that into
// the next child's argv and environment — two destinations the tag and the registry
// do not have. The controls are the same two and they are enough: the anchored
// ValidStem match upstream excludes a leading dash, every separator and any length,
// so the value can be neither read as a flag nor joined into a path, and nothing
// here or in streamsup hand-rolls a join — the spawn's --session-id / --resume
// choice stays useCreateForm's, whose StatByID probe runs ValidStem itself first.
// What keeps the extension INSIDE the boundary rather than beside it is that the
// runner is moved on the far side of the unwind: a refused id reaches the tag, the
// registry and the runner alike not at all.
// sessionTag is the follower's view of the runner's live session tag: read the id
// being replaced, and move the tag only while it still holds it. Two methods, defined
// at the consumer, and *streamSessionTag is the only production implementation —
// widening the field below from that concrete type changed no call site.
//
// It exists for a testing need #2176 could not meet otherwise. The read in follow and
// the conditional write that follows it are adjacent statements on one goroutine, so
// no external double sits between them and the interleaving where a competing
// rotation lands in that gap is not injectable. A double that performs the competing
// rotation at the top of its CompareAndSwap and then delegates IS that ordering,
// because the follower's goroutine does nothing else in between.
type sessionTag interface {
	ID() string
	CompareAndSwap(oldID, newID string) bool
}

type sessionResetFollower struct {
	// tag is the runner's live session tag — both the source of the id being
	// replaced and the thing replaced. Never a construction-time id: a captured one
	// would be right for exactly one reset and wrong for every later one.
	tag sessionTag
	// adopt re-keys the pool session, from sessions.RunnerConfig.AdoptAnnouncedReset.
	// nil calls nothing — the tag still rotates and the runner still adopts, which are
	// the halves this type owns.
	adopt func(oldID, newID string) error
	// adoptRunner installs the announced id as the runner's live spawn id, from
	// (*streamsup.Runner).AdoptSessionID (#2136). Without it the pool, the tag and
	// the drain's gate all move onto the announced id while the runner keeps the
	// pre-reset one, and its next crash respawn --resumes the conversation the
	// announcement cleared.
	//
	// It is assigned AFTER construction, unlike every other field here, because the
	// runner does not exist yet when this type is minted: newStreamRunnerFactory
	// builds the follower above streamsup.New, since the follower's Sink is what the
	// parser writes into and that parser is the runner's Stdout. The factory closes
	// the loop on the line below that constructor — inside the single-goroutine
	// window it already occupies, and before the sessions layer starts Run, so the
	// goroutine-creation edge publishes it to the stdout forwarder that later reads
	// it. Never written again.
	//
	// nil calls nothing, matching adopt and next. Production always supplies it; a
	// test follower that leaves it nil exercises the tag half alone.
	adoptRunner func(newID string)
	// next is the downstream sink every event is forwarded to, unchanged. nil
	// forwards nothing — a test convenience; production always supplies
	// streamTurnSink.sinkForTag's closure.
	next   func(turnevent.Event)
	logger *slog.Logger
}

// newSessionResetFollower mints a follower that forwards to next. A nil logger
// falls back to slog.Default, matching newStreamTurnSink.
func newSessionResetFollower(tag sessionTag, adopt func(oldID, newID string) error,
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
// The tag is written BEFORE the pool call and UNWOUND afterwards unless the pool's
// answer means the session is now on the announced id. The rule stated positively,
// because it is the rule and not the sentinel list that must survive a new sentinel
// being added: the tag ends on newID exactly when adopt answers nil, meaning this
// call re-keyed; every other answer means NOTHING moved, and the tag goes back.
// rekeyPool is where that one question is answered, so neither step below re-derives
// it from a sentinel list.
//
// BOTH writes are conditional (#2176), and that is this type's second half of the
// trust boundary alongside the unwind. Each is a compare-and-swap against the value
// this call read, so a write lands only while the tag still holds it. The tag has a
// second writer on another goroutine — streamsup.Config.OnSessionRotate, which the
// factory installs as the tag's plain Rotate and which RestartFresh fires after a
// daemon-driven RotateForNewSession or RotateBootstrapForSelfHeal already re-keyed
// the registry. That writer DROVE its rotation and is the authority; this one is
// FOLLOWING one claude announced, so it must never overwrite it. A store would, in
// either direction: the forward write would put the tag back on an announced id the
// registry never went to, and the unwind would put it back on a retired one the
// registry has left. Both end with the tag and the registry naming different
// sessions, which is the drain's active-session gate dropping every later event —
// the dark conversation this seam exists to prevent, reached by the two paths a
// plain unwind would have opened.
//
// A failed swap is therefore not an error: it is another rotation having won, and
// the winner's id is left standing. The follower declines the whole announcement
// there — no pool call on the forward path, and no runner adopt on either.
//
// The RUNNER then follows the tag, and #2136's rule is a biconditional over that
// same answer: the runner's id ends on the announced id exactly when the tag does.
// That is why it is one call on the far side of the unwind rather than one beside
// each qualifying answer — a sentinel added later to AdoptAnnouncedID is then
// answered once, by rekeyPool, with no second list to keep in step. Its inverse is
// a distinct regression from the tag's: the runner's id is what beginSpawn feeds to
// each spawn's argv, so a runner left on the pre-reset id spawns
// --resume <pre-reset> on its next crash and silently resurrects the conversation
// the announcement cleared, while every other reader of the daemon believes it is
// in the new one.
//
// The runner adopts AFTER the pool's answer, where the tag rotated before it, which
// leaves a window in which the tag reads the announced id and the runner still holds
// the previous one. A crash inside it resumes the pre-reset transcript — today's
// behaviour, and strictly better than resuming a DIFFERENT live session's, which is
// what adopting before the answer would risk on the refusal path. It is left open
// deliberately; there is no rollback here.
//
// Writing first, rather than after a successful call: the pool call rebinds the
// owning conversation, and the rebind is what makes the old tag dead. Doing it
// first means no other goroutine can ever observe the rebind while the tag still
// reports the id it retired — a window that would otherwise open on EVERY
// successful reset, spanning a registry write and the observer fan-out. Making the
// write conditional does not disturb that ordering: a compare-and-swap before the
// call is still a write before the call.
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
// announcement is ignored instead: registry unchanged, one Warn, and the tag back on
// the id this call read — or, where a rotation won in between, on that winner's,
// which is the one place the unwind deliberately does not restore what it read.
//
// The unwind leaves one window and it is bounded and strictly smaller than the one
// it replaces. Between the write and the unwind the tag reads newID, and
// exitForTag runs on the runner's supervision goroutine rather than this one, so a
// child exiting exactly there would be reported under the announced id. That window
// spans only the declining paths, which return before any disk write — a lock
// acquisition and two map probes. Writing after the call instead would trade it
// for a window on every SUCCESSFUL reset, spanning the registry save and the
// observer fan-out, and that is the common path.
//
// Everything else runs SYNCHRONOUSLY on claude's stdout forwarder goroutine, which
// is also the goroutine that produces every later EVENT of this session, so no event
// can be produced between the steps. That is a statement about events and not about
// the tag: the tag's other writer above is a different goroutine, which is the whole
// of #2176. An asynchronous hand-off would reopen the event window; it was rejected
// for that, not for cost.
func (f *sessionResetFollower) follow(newID string) {
	oldID := f.tag.ID()
	if newID == oldID {
		return
	}
	if !f.tag.CompareAndSwap(oldID, newID) {
		// Another rotation moved the tag between the read above and this write. It re-keyed
		// the registry itself, so its id is the one both halves must end on: the pool is
		// never asked and the runner never adopts.
		f.superseded(oldID, newID, nil)
		return
	}
	if err := f.rekeyPool(oldID, newID); err != nil {
		// The pool moved nothing, so the tag goes back BEFORE the record is written and the
		// runner is never asked to adopt — but only while the tag still holds the id this
		// call put there, per the conditional-write rule above.
		f.tag.CompareAndSwap(newID, oldID)
		if errors.Is(err, sessions.ErrSessionNotFound) {
			// oldID was gone by the time the pool looked, so the session moved somewhere else
			// or is gone. Since #2137 retired the rotation watcher this can no longer mean the
			// session moved HERE — every surviving producer re-keys onto a MINTED id
			// (RotateForNewSession, RotateBootstrapForSelfHeal) or leaves no successor at all
			// (Remove, the idle-eviction sweep, the create-rollback deletes).
			f.superseded(oldID, newID, err)
			return
		}
		// ErrSessionIDTaken today, and any sentinel added later — which lands here rather
		// than above, on the side that declined and unwound, because that is the safe
		// default. Warn rather than superseded's Info: this one is claude naming an id the
		// daemon cannot follow it to, which is either a defect or a confused child.
		f.logger.Warn("relay: announced reset refused, tag left on the previous id",
			"event", "announced_reset.refused",
			"session_id", newID,
			"previous_session_id", oldID,
			"err", err)
		return
	}
	if f.adoptRunner != nil {
		f.adoptRunner(newID)
	}
}

// superseded writes the one record for an announcement declined because another
// rotation won, from either of the two places that can discover it: the forward write
// losing its swap, and the pool answering that oldID is gone. They share an event key
// because they are one operator-facing fact — a daemon-driven rotation got there first
// and the follower correctly stood down — and current_session_id, the id the tag
// actually ends on, is what separates them. err is present only where a pool answer
// produced the decline, which is the other half of that separation.
//
// It replaces the announced_reset.already_applied record #2135 shipped. That name
// asserted the session had moved onto the announced id, which is what #2176 found no
// remaining code path can make true; keeping it would have named a case that is no
// longer "already applied".
//
// Info rather than already_applied's Debug: this is a lifecycle event — a rotation
// stood down — and it is rare. The Debug choice was argued from an operator being
// unable to tell #2176's divergence from a benign removal by reading it, and that
// divergence is what this record now reports the absence of. It stays below the Warn
// above, which remains the record that always matters.
//
// SECURITY: content-free, exactly as the record it replaces was. The three ids are
// either daemon-minted or already validated as a canonical UUID stem by streamsup's
// emitConversationReset, and err can only be a bare package sentinel — both
// RunnerConfig.AdoptAnnouncedReset literals return AdoptAnnouncedID's error unwrapped,
// so no text claude authored can reach it. The event itself is never logged, and no
// decode error can reach here: emitConversationReset deliberately has no logging
// surface at all, because encoding/json quotes the offending input into its error text.
func (f *sessionResetFollower) superseded(oldID, newID string, err error) {
	attrs := []any{
		"event", "announced_reset.superseded",
		"session_id", newID,
		"previous_session_id", oldID,
		"current_session_id", f.tag.ID(),
	}
	if err != nil {
		attrs = append(attrs, "err", err)
	}
	f.logger.Info("relay: announced reset superseded, tag left on the id the registry holds", attrs...)
}

// rekeyPool runs the pool half of the adoption and answers the ONE question both
// remaining steps need: does the session now stand on newID? nil says it does and
// the pool's error says it does not, which is what lets follow express the unwind
// and the runner adopt as one branch instead of replicating a sentinel list at each.
//
// Two answers mean it does, and they are two different situations rather than one
// wearing two faces. adopt == nil is a runner built without a pool callback, where
// there is no re-key to wait for and the tag half is the whole of it. A nil error is
// this call having re-keyed — since #2137 that is the ordinary answer, and this call
// is the only thing that applies an announced reset.
//
// It used to be three. ErrSessionNotFound — oldID gone by the time the pool looked —
// answered nil here too, on the reading that the session had already been moved onto
// newID by whoever got there first. The rotation watcher was that whoever: it fired on
// the new transcript's creation, observed the SAME announced rotation, and re-keyed
// onto the SAME id. #2137 retired it and the premise went with it. Every surviving
// producer of the sentinel re-keys onto a MINTED id or leaves no successor at all, so
// on today's code it can only mean the session moved somewhere ELSE or is gone. #2176
// deleted the arm: the sentinel now flows out as the error it is, and follow declines
// and unwinds on it like any other. What made that safe to do — and what the earlier
// reading was protecting against — is that follow's unwind is now conditional, so the
// tag is left on the winner's id rather than dragged back to a retired one.
//
// The one question stays keyed to the ANSWER rather than to a sentinel list, so a
// sentinel added later to AdoptAnnouncedID still lands on the declining side by
// default. Returning the pool's error unwrapped keeps follow's records naming what the
// pool actually answered; the only sentinel follow inspects, it inspects to pick a
// record's event key, never to decide whether the session moved.
func (f *sessionResetFollower) rekeyPool(oldID, newID string) error {
	if f.adopt == nil {
		return nil
	}
	return f.adopt(oldID, newID)
}

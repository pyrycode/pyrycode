package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// This file holds the daemon side of the conversation reset's wrap-up turn
// (#2477): before a new_session replaces a live claude, the outgoing session is
// asked to write a handoff note for its successor, and that reply becomes the
// note #2475 already composes into the successor's system prompt.
//
// IT IS contextUsageResolver'S SHAPE, and deliberately: a daemon-side coordinator
// over the daemon context, with injected seams, that waits for the conversation to
// go idle, makes one bounded round trip to the child, owns the nil-tracker check
// WaitIdle does not carry, and logs no content. What differs is the direction —
// that one asks the child a question and answers a client, this one asks the child
// for prose and writes it to disk — and the consequence of failing, which here is
// nothing: a wrap-up that does not produce a note must never fail the reset it was
// run for.
//
// WHY THE RESET RUNS OFF THE CALLER'S GOROUTINE. relay's handleNewSession calls
// SessionStarter.StartNewSession INLINE on the V2SessionManager's single Run
// dispatch goroutine, as its own doc says. This routine is bounded at ninety
// seconds. Running it there would freeze frame dispatch for every connection and
// every conversation the daemon hosts for as long as one background chat took to
// write a paragraph. So StartNewSession hands the tail to a goroutine and returns;
// begin below is what keeps a second frame from starting a second one.

const (
	// wrapUpDeadline bounds the WHOLE wrap-up: the idle wait, the write, and the
	// reply. On expiry the reset proceeds and the previous note stands (AC 2).
	//
	// NINETY SECONDS IS A CEILING ON THE OPERATOR'S WAIT, not an estimate of the
	// turn. A reset is a foreground gesture — somebody pressed New session and is
	// watching — so the bound has to be short enough that a silent child does not
	// read as a hung daemon, and long enough that an ordinary wrap-up (a few
	// hundred words, no tools) finishes inside it with room for a busy turn to be
	// interrupted first. It is Juhana's figure, fixed in the daemon with the prompt
	// and not operator-editable, for the reason the prompt is: an operator-supplied
	// bound would be a way to hold a child open.
	//
	// It bounds the idle wait TOO, which is where it departs from
	// contextUsageQueryTimeout's division ("it bounds the query and not the turn
	// wait"). That verb answers a question and may honestly wait out any turn; this
	// one is replacing the child either way, so a turn that will not end is a reason
	// to get on with the reset rather than to keep waiting.
	wrapUpDeadline = 90 * time.Second

	// wrapUpPromptText is the turn the outgoing session is asked to answer. FIXED IN
	// THE DAEMON and not operator-editable, which is this feature's main containment:
	// no operator and no client text enters it, so the only untrusted bytes in the
	// composed prompt are the previous note's, and those are fenced and admitted.
	//
	// The last clause — anything the user said that the session did not save itself —
	// is there because the vault's knowledge sweep runs on a schedule and a fact
	// mentioned in passing may not be filed when the successor starts minutes later.
	// Repeating something the sweep files anyway is harmless: the note is capped by
	// the store and overwritten at the next reset.
	wrapUpPromptText = "This conversation is being reset. You are the outgoing session. " +
		"Reply with a handoff note for the session that replaces you, and nothing else: " +
		"no tool calls, no questions, no preamble. If a note from an earlier session " +
		"appears below, keep what is still live and drop what is finished. Cover, in this " +
		"order: what was being worked on and its current state, decisions made and why, " +
		"what is unfinished and the next step for each, and anything the successor should " +
		"avoid or not repeat. Then add anything the user told you this session that you did " +
		"not save yourself, so it survives until it is filed. Plain prose or short bullets. " +
		"At most 400 words."

	// wrapUpPreviousNoteHeading titles the previous note's section. It is the
	// wrap-up prompt's counterpart to handoffNoteLead and deliberately shorter: the
	// sentence that governs how the note is READ is already in the prompt above
	// ("keep what is still live and drop what is finished"), and it is composed
	// FIRST, so the reader meets the instruction before the material. A second
	// governing sentence here would either repeat it or disagree with it.
	wrapUpPreviousNoteHeading = "Previous handoff note"
)

// composeWrapUpPrompt builds the turn: the fixed prompt, and — when there is an
// admissible previous note — a heading and the note inside the same fence the
// system-prompt path uses.
//
// THE NOTE IS ADMITTED, NOT TRUSTED, and the admission is #2475's rather than a
// second one written here. Pool.HandoffNote's own contract says the text it
// returns is claude-authored, crossed the subprocess boundary, and was judged for
// its size and its mode and nothing else — the obligation is the composing site's,
// and this is a composing site. sessions.FencedHandoffNote is that obligation
// discharged by the one predicate that already exists: a second predicate over the
// same bytes is how the two drift apart, and a marker spelled by hand here would
// leave the refusals guarding a shape this composition does not have.
//
// A REFUSED NOTE YIELDS THE BARE PROMPT — the fail-closed direction, and the same
// answer handoffNoteSection gives. The wrap-up still runs: losing the predecessor's
// note costs the writer its pruning context, while skipping the turn would cost the
// successor everything this session knows.
func composeWrapUpPrompt(previous string) string {
	fenced, ok := sessions.FencedHandoffNote(previous)
	if !ok {
		return wrapUpPromptText
	}
	return wrapUpPromptText + "\n\n" + wrapUpPreviousNoteHeading + "\n" + fenced
}

// wrapUpCapturer is the optional per-runner capability streamRunner exposes. It
// stays OFF sessions.Runner for contextUsageQuerier's stated reason: its only
// consumer is this file, and widening that interface would pull every test double
// in the tree into the slice.
type wrapUpCapturer interface {
	BeginWrapUp() (*wrapUpReply, func(), bool)
}

// resetBacklog is the msgqueue half, defined at the consumer and narrow on
// purpose: the reset drops a conversation's queued messages and nothing else, so
// it names the read and the drop and no other queue operation.
type resetBacklog interface {
	Snapshot(convID string) []msgqueue.QueuedMessage
	Remove(convID string, id uint64) bool
}

// handoffNoteStore is the store half. Two methods off *sessions.Pool, named at the
// consumer so the coordinator's tests need no pool and no data directory.
type handoffNoteStore interface {
	HandoffNote(id conversations.ConversationID) (string, error)
	WriteHandoffNote(id conversations.ConversationID, text string) (string, error)
}

// resetTarget is one conversation's live child as this routine needs it: somewhere
// to write the turn, and the runner to interrupt and to arm the capture on.
//
// The WRITE is a func rather than the *sessions.Session it comes from, the
// division contextUsageResolveFunc keeps: the seam crossing into this type carries
// no internal/sessions value the coordinator would otherwise have to construct in
// a test. The RUNNER is the interface, because two separate optional capabilities
// are asserted off it and naming both here would make them look mandatory.
type resetTarget struct {
	write  func(ctx context.Context, conversationID string, payload []byte) error
	runner sessions.Runner
}

// conversationReset runs the wrap-up turn and owns the one-reset-at-a-time guard.
//
// Every seam is optional and every one of them degrades rather than panicking: an
// unwired daemon (the PTY posture) runs no wrap-up and rotates exactly as it did
// before this ticket. That is the posture every optional seam in this binary keeps,
// and here it is what makes the whole feature additive.
type conversationReset struct {
	// base owns every wrap-up. NOT the caller's context, contextUsageResolver.fly's
	// rule applied to a stricter case: the caller here is a relay dispatch goroutine
	// that returns immediately, so a caller-scoped context would cancel the wrap-up
	// before it began. Daemon shutdown is what reaps these goroutines.
	base context.Context

	// resolve answers the conversation's live child, re-run on the reset goroutine
	// rather than taken from the caller. The binding can move across the hand-off —
	// that is a ninety-second window now, not a microsecond one — and the write
	// surface is the bound *sessions.Session, which the starter's own resolve seam
	// does not carry.
	resolve func(convID string) (resetTarget, bool)

	// busy is the idle gate. Optional: nil is the PTY wiring, where the tracker is
	// never constructed. WaitIdle carries no nil-receiver guard of its own, so this
	// type owns that check — the bargain contextUsageResolver and
	// waitIdleForDelivery both keep.
	busy *turnBusyTracker

	backlog resetBacklog
	notes   handoffNoteStore

	// deadline bounds one wrap-up; zero means wrapUpDeadline. Named so a test can
	// cross it without sleeping out a real ninety seconds.
	deadline time.Duration

	log *slog.Logger

	// mu guards inFlight and is a LEAF: nothing is called while it is held.
	mu       sync.Mutex
	inFlight map[string]struct{}
}

// newConversationReset wires the production coordinator, or nil when the daemon
// has no registry or no pool — the foreground / PTY posture, where
// activeSessionStarter.reset stays nil and the rotation is the synchronous
// pre-#2477 one.
//
// busy and queue may each be nil and are handled at their own use sites rather
// than here: PTY mode constructs no tracker, and a queue is nil only in wirings
// that never enqueue. A NIL *msgqueue.Queue IS NOT ASSIGNED INTO THE INTERFACE,
// because a typed nil in an interface is non-nil at the interface level and would
// route straight past dropBacklog's guard into a method call on nothing — the
// hazard turnBusyTracker.observe's doc names for its own concrete parameter.
func newConversationReset(
	base context.Context,
	convReg *conversations.Registry,
	pool *sessions.Pool,
	busy *turnBusyTracker,
	queue *msgqueue.Queue,
	log *slog.Logger,
) *conversationReset {
	if convReg == nil || pool == nil {
		return nil
	}
	r := &conversationReset{
		base: base,
		resolve: func(convID string) (resetTarget, bool) {
			// resolveBoundSession, not resolveBoundRunner: this path needs the bound
			// *Session's write surface as well as its runner, and its
			// CurrentSessionID == "" guard is the #678 isolation enforcement point —
			// without it Pool.Lookup("") answers the BOOTSTRAP session and a wrap-up
			// prompt would be written into the shared child.
			sess, _, _, ok := resolveBoundSession(convReg, pool, convID)
			if !ok {
				return resetTarget{}, false
			}
			return resetTarget{write: sess.WriteUserTurn, runner: sess.Runner()}, true
		},
		busy:  busy,
		notes: pool,
		log:   log,
	}
	if queue != nil {
		r.backlog = queue
	}
	return r
}

func (r *conversationReset) logger() *slog.Logger {
	if r == nil || r.log == nil {
		return slog.Default()
	}
	return r.log
}

func (r *conversationReset) bound() time.Duration {
	if r == nil || r.deadline <= 0 {
		return wrapUpDeadline
	}
	return r.deadline
}

// begin claims convID for one reset and returns the release the caller must run.
// A second claim while one is live is REFUSED (AC 4) — dropped, not queued: a
// queued second reset would run another wrap-up turn against a child the first one
// is about to replace, spending tokens to write a note over the one just written.
//
// The check and the insert happen under ONE lock acquisition, so two frames
// arriving together cannot both claim. Per CONVERSATION and not daemon-wide: two
// operators resetting two chats are doing two independent things.
//
// It is the conversation-level half of the guard. wrapUpCapture.begin is the
// runner-level half and neither subsumes the other — this one is keyed by
// conversation, that one by runner, and a rebinding moves one without the other.
//
// A nil receiver and an empty id both refuse, so an unwired daemon starts nothing
// and an unresolved conversation never becomes a key.
func (r *conversationReset) begin(convID string) (func(), bool) {
	if r == nil || convID == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, running := r.inFlight[convID]; running {
		return nil, false
	}
	if r.inFlight == nil {
		r.inFlight = make(map[string]struct{})
	}
	r.inFlight[convID] = struct{}{}
	return func() { r.release(convID) }, true
}

// release drops the claim. Run from the reset goroutine's defer, so a panic in the
// routine cannot leave a conversation permanently unable to reset.
func (r *conversationReset) release(convID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, convID)
}

// wrapUp runs the whole routine for convID and returns nothing, which is the
// contract rather than an omission: AC 2 says a wrap-up that fails, times out,
// produces nothing usable or meets a disabled store leaves the previous note
// standing and does NOT fail the reset — so there is no failure a caller could act
// on, and a returned error would invite one to try.
//
// EVERY STEP'S POSITION IS LOAD-BEARING:
//
//  1. The backlog is dropped FIRST. msgqueue's drain delivers on the conversation's
//     own goroutine as soon as the conversation goes idle, and the idle window this
//     routine is about to open is exactly the one it is waiting for. Dropping first
//     is what keeps a queued message out of the wrap-up turn (AC 5).
//  2. The conversation is resolved on THIS goroutine; see the resolve field.
//  3. The interrupt, then the idle wait. The wait is the real gate — the interrupt
//     only makes it come sooner — which is why an inert or failing interrupt is
//     tolerated rather than aborting.
//  4. The capture is armed BEFORE the write, so no chunk of the reply can precede
//     it. This is the ordering the whole decorator exists to make possible.
//  5. The conversation is marked mid-turn before the write and unmarked on a write
//     error, which is newInboundDeliver's sequence and its argument verbatim: the
//     tracker's opener feed is asynchronous, so a send_message arriving here would
//     otherwise find the conversation idle and deliver straight into the wrap-up turn.
//
// THE #1330 ROTATION GATE IS DELIBERATELY NOT ARMED ACROSS ANY OF THIS.
// startFreshRunner arms it, and it runs after this returns; armed here, every
// WriteUserTurn below would answer the retryable ErrNoLiveChild and the wrap-up
// could never be delivered at all.
func (r *conversationReset) wrapUp(convID string) {
	if r == nil || r.resolve == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.baseContext(), r.bound())
	defer cancel()

	r.dropBacklog(convID)

	target, ok := r.resolve(convID)
	if !ok {
		// Resolvable at dispatch and not now: the binding moved, or the session is
		// gone. The reset still proceeds; there is simply nobody left to ask.
		r.logger().Debug("relay: reset wrap-up skipped; conversation has no bound session",
			"event", "reset.wrapup.unresolved",
			"conversation_id", convID)
		return
	}
	capturer, ok := target.runner.(wrapUpCapturer)
	if !ok {
		r.logger().Debug("relay: reset wrap-up skipped; bound runner captures no reply",
			"event", "reset.wrapup.no_capture",
			"conversation_id", convID)
		return
	}

	// Tolerated on both arms. arm is logged as a plain string, never the named type:
	// slog renders a named string type through the Any path and this binary's
	// JSONHandler and relay's TextHandler do not agree on how that renders — the
	// reason activeInterrupter.SendEsc gives for the same cast.
	if arm, err := interruptRunner(target.runner); err != nil || arm == armNone {
		r.logger().Debug("relay: reset wrap-up interrupt was inert or failed; waiting for idle anyway",
			"event", "reset.wrapup.interrupt_inert",
			"conversation_id", convID,
			"arm", string(arm),
			"err", err)
	}
	if r.busy != nil {
		if err := r.busy.WaitIdle(ctx, convID); err != nil {
			r.logger().Debug("relay: reset wrap-up abandoned; the conversation did not go idle in time",
				"event", "reset.wrapup.not_idle",
				"conversation_id", convID)
			return
		}
	}

	reply, stop, armed := capturer.BeginWrapUp()
	if !armed {
		// A capture is already live on this runner — another reset in flight against
		// the same session under a different conversation key. Declining is correct:
		// two captures would split one turn's text and write two half-notes.
		r.logger().Debug("relay: reset wrap-up skipped; a reply capture is already armed",
			"event", "reset.wrapup.capture_busy",
			"conversation_id", convID)
		return
	}
	defer stop()

	undo := r.busy.openForDelivery(convID)
	if err := target.write(ctx, convID, []byte(composeWrapUpPrompt(r.previousNote(convID)))); err != nil {
		undo()
		// SECURITY: the error value is deliberately NOT recorded; see the rule in
		// storeNote. This is the site that makes the rule necessary rather than
		// merely prudent — the payload this call carried IS the composed prompt,
		// previous note included, so an error wrapping any part of a rejected
		// payload puts note bytes into a record through a channel no reviewer would
		// think to inspect. resolveSpawnDir keeps the same posture for its
		// confinement error: the record names the conversation and nothing else.
		r.logger().Warn("relay: reset wrap-up prompt was not delivered",
			"event", "reset.wrapup.write_failed",
			"conversation_id", convID)
		return
	}
	text, ended := reply.wait(ctx)
	if !ended {
		r.logger().Warn("relay: reset wrap-up hit its deadline; the previous handoff note stands",
			"event", "reset.wrapup.deadline",
			"conversation_id", convID)
		return
	}
	r.storeNote(convID, text)
}

// baseContext answers the daemon context, or Background for a literal built
// without one — the tolerance newContextUsageResolver gives the same field.
func (r *conversationReset) baseContext() context.Context {
	if r.base == nil {
		return context.Background()
	}
	return r.base
}

// dropBacklog empties convID's queue so nothing is delivered into the wrap-up turn
// and the emptied queue_state reaches clients (AC 5).
//
// NO SECOND PUBLISH IS NEEDED: Queue.Remove already fires the change notify that
// republishes queue_state, so the frame the operator sees is the queue's own
// record of what happened rather than a claim this file makes alongside it.
//
// A REFUSAL IS TOLERATED, and it is the right answer rather than a gap. Remove
// declines the head while it is committing — past the idle gate and being written
// — and that message is already going into the outgoing child, so there is nothing
// left to drop. AC 5 asks for the backlog to be dropped, not for Remove to be made
// total. After the interrupt above this is the rare case anyway.
//
// It iterates a SNAPSHOT rather than re-reading, so a message enqueued while this
// runs is left alone: it arrived after the operator asked for a clean slate, and
// the queue's own drain will hold it behind the idle gate the wrap-up turn owns.
func (r *conversationReset) dropBacklog(convID string) {
	if r.backlog == nil {
		return
	}
	queued := r.backlog.Snapshot(convID)
	dropped := 0
	for _, m := range queued {
		if r.backlog.Remove(convID, m.ID) {
			dropped++
		}
	}
	if len(queued) == 0 {
		return
	}
	// Counts, never text: a queued message is the operator's own words and this
	// record names how many there were, not what they said.
	r.logger().Debug("relay: reset dropped the conversation's queued backlog",
		"event", "reset.backlog_dropped",
		"conversation_id", convID,
		"queued", len(queued),
		"dropped", dropped)
}

// previousNote reads the note the wrap-up prompt asks the writer to prune, or ""
// when there is none, the store is disabled, or the read failed.
//
// A FAILED READ IS NOT A FAILED WRAP-UP. It costs the writer its pruning context
// and nothing else, while skipping the turn would cost the successor everything
// this session knows — the same trade MaxHandoffNoteBytes' doc makes when it
// truncates rather than refuses.
func (r *conversationReset) previousNote(convID string) string {
	if r.notes == nil {
		return ""
	}
	note, err := r.notes.HandoffNote(conversations.ConversationID(convID))
	if err != nil {
		// SECURITY: the value is not recorded; see storeNote for the rule.
		r.logger().Warn("relay: reset could not read the previous handoff note; composing without it",
			"event", "reset.wrapup.note_read_failed",
			"conversation_id", convID)
		return ""
	}
	return note
}

// storeNote writes the reply as the conversation's handoff note, or leaves the
// previous one standing (AC 2).
//
// IT ADMITS THE REPLY WITH THE READ SIDE'S OWN PREDICATE, and that is the finding
// this ticket's security pass turned up rather than a flourish. A reply that
// #2475's handoffNoteSection would refuse — blank, invalid UTF-8, or carrying a
// forged fence marker — composes into NO SECTION AT ALL when the successor spawns.
// Storing one therefore destroys a usable predecessor note and hands the successor
// nothing, silently, with every record on the path saying the reset went fine.
// Judging it here makes the outcome the same as an empty reply's, which is the
// outcome AC 2 already specifies, and it costs no second predicate: it is the very
// function the composing site will call.
//
// NO ERROR VALUE ON THIS PATH REACHES A RECORD, and the rule is absolute rather
// than reasoned per call site. Today Pool.HandoffNote and Pool.WriteHandoffNote
// wrap paths and OS errors and carry no note bytes — their own docs say so — but
// AC 3 asks for "no fragment at any level", and a prohibition that holds only
// because another package currently behaves is one a later wrap silently ends.
// Every record here therefore carries an event key and the conversation id, and
// the keys are kept granular (note_read_failed / note_write_failed /
// notes_disabled / reply_unusable) so an operator can still tell the four apart —
// which, with the path being derivable from the conversation id, is what the error
// value was buying.
//
// WHAT IS STORED IS THE RAW REPLY, never the fenced rendering. The fence belongs to
// a composing site and the store holds the note verbatim — writing the frame to
// disk would put a marker inside the note that the next admission then refuses.
//
// The blank case needs no arm of its own: admissibleHandoffNote refuses a note that
// is blank after trimming, which is the first thing it tests.
func (r *conversationReset) storeNote(convID, text string) {
	if r.notes == nil {
		return
	}
	if _, ok := sessions.FencedHandoffNote(text); !ok {
		// Content-free by construction: the record says the reply was unusable and
		// never why in the reply's own bytes.
		r.logger().Warn("relay: reset wrap-up produced no usable note; the previous one stands",
			"event", "reset.wrapup.reply_unusable",
			"conversation_id", convID)
		return
	}
	if _, err := r.notes.WriteHandoffNote(conversations.ConversationID(convID), text); err != nil {
		if errors.Is(err, sessions.ErrHandoffNotesDisabled) {
			// Not a failure: persistence is off, so there is nowhere a note could live.
			// Debug, and without the sentinel's text, because the event key says it.
			r.logger().Debug("relay: reset wrap-up note not stored; handoff notes are disabled",
				"event", "reset.wrapup.notes_disabled",
				"conversation_id", convID)
			return
		}
		r.logger().Warn("relay: reset wrap-up note could not be stored; the previous one stands",
			"event", "reset.wrapup.note_write_failed",
			"conversation_id", convID)
		return
	}
	// The one Info on the happy path, and it carries no path: WriteHandoffNote
	// returns where it wrote, and the location of a conversation's note is a fact
	// about the operator's filesystem that the event key and the conversation id
	// already imply.
	r.logger().Info("relay: reset wrote the outgoing session's handoff note",
		"event", "reset.wrapup.note_written",
		"conversation_id", convID)
}

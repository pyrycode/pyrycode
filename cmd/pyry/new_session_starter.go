package main

import (
	"log/slog"
	"strings"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// startFreshRunner is the new_session twin of interruptRunner: it dispatches a
// fresh-session start to the active conversation's bound runner through
// sessions.Runner — rotate the pool-side id then RestartFresh so the next spawn
// uses --session-id <newID>, with NO /clear keystroke. RestartFresh and
// BeginRotation are ON the interface since #2592, so there is no inert arm: the
// type assertions this replaced let a runner lacking either method silently rotate
// nothing, or rotate ungated.
//
// Ordering is load-bearing: rotate() completes — the pool-side re-key published
// under Pool.mu — BEFORE RestartFresh spawns <newID>.jsonl. Its original reason was
// the rotation watcher, which had to observe the id registered as freshly allocated
// before it saw the CREATE, or it double-rotated; #2137 retired the watcher and the
// skip-set, and the reason that survives is #1330's, below.
//
// That order is PRESERVED at #1330; the rotation gate is armed AHEAD of both, not
// substituted for either. rotate() also fires the ReasonClear transition fan-out,
// so without the arm the clear reaches clients — and releases whatever the turn
// tracker held — while the outgoing child is still alive and the fresh one does
// not yet exist. A turn accepted in that ~4 ms window writes into the doomed
// child, msgqueue reads the successful write as a commit and drops the head, and
// the turn is gone. Arming first makes every WriteUserTurn in the window return
// the retryable ErrNoLiveChild instead, so msgqueue re-attempts the same head
// until the fresh child binds.
// spawnDir, when non-empty, is the directory the successor must come up in —
// already re-confined by resolveSpawnDir at the caller (#1475). "" means "leave
// the runner where it is", which is both the no-recorded-workspace case and the
// refused case; either way the rotation still completes and the child still comes
// up. Installing it is an OPTIONAL capability (installSpawnDir): a runner that
// cannot move still rotates, just in place.
//
// THE INSTALL SITS BETWEEN rotate AND RestartFresh, the window
// refreshSystemPromptForRotation occupies, and both edges are load-bearing.
// Below rotate, because a FAILED rotation must leave the runner untouched:
// installed above it, a rotate error would leave the directory swapped and the
// next crash-respawn would silently move a child no rotation ever replaced —
// which is exactly the "change_workspace alone does not move a running child"
// promise, broken from a remotely-driven frame that merely lost a race. Above
// RestartFresh, because that call cancels the live child at once and the Run loop
// answers on its own goroutine, so an install landing after it races the
// successor's own beginSpawn and the loser comes up in the pre-move directory.
//
// The resolve itself is deliberately NOT here. It does filesystem I/O, and the
// #1330 gate is armed across this whole function: every WriteUserTurn on the
// conversation returns ErrNoLiveChild while it is, so a blocking syscall inside
// the armed window would widen a ~4 ms refusal into a filesystem's worth.
// StartNewSession resolves before it calls, below all of its inert arms.
// log records the install's own failure and must be the daemon's, not
// slog.Default(): cmd/pyry never calls slog.SetDefault, so a default-logger record
// would leave the daemon's log entirely. Nil is tolerated and discards, which is
// what keeps the direct test callers free of a logger they have no assertion for.
func startFreshRunner(r sessions.Runner, oldID sessions.SessionID, spawnDir string,
	rotate func(sessions.SessionID) (sessions.SessionID, error), log *slog.Logger) error {
	abort := r.BeginRotation()
	newID, err := rotate(oldID)
	if err != nil {
		// The rotation never happened, so the gate must not outlive it: left
		// armed it would refuse every turn on this conversation until the next
		// respawn — a wedge the failed rotation never earned. Losing a race with
		// a concurrent new_session frame is the ORDINARY way to land here
		// (RotateForNewSession's ErrSessionNotFound), which is exactly why the
		// disarm is generation-stamped runner-side: it must not clear the winner's
		// arm.
		abort()
		return err
	}
	installSpawnDir(r, spawnDir, log)
	r.RestartFresh(string(newID))
	return nil
}

// installSpawnDir moves the runner's next spawn into spawnDir when the runner can
// be moved and there is somewhere to move it to (#1475); anything else is inert.
//
// An OPTIONAL assertion, deliberately, unlike RestartFresh and BeginRotation,
// which sessions.Runner carries (#2592): a runner that cannot move still rotates
// correctly, just in its current directory, so a missing method here loses a
// workspace move rather than the rotation itself.
//
// The install's own failure is Warned and SWALLOWED. It is reachable only when
// the directory disappears between the caller's confinement and this call, and by
// then the rotation is already committed — the pool is re-keyed, the conversation
// rebound, the transition broadcast — so refusing to respawn would leave the
// conversation with no child at all. Fail-closed means keeping the old directory,
// not withholding the successor. It is a DISTINCT event from the caller's
// rejection record: "refused by confinement" and "vanished before the install"
// are different failures and one event name for both would be unreadable.
//
// SECURITY: spawnDir is resolveSpawnDir's confined output or "". No path reaches
// the log — the record names the conversation and nothing else, the posture
// Pool.Revive's contract and sessionTranscriptDir's SECURITY paragraph both hold
// for phone-influenced workspace paths.
func installSpawnDir(r sessions.Runner, spawnDir string, log *slog.Logger) {
	if spawnDir == "" {
		return
	}
	m, ok := r.(interface{ SetSpawnWorkDir(string) error })
	if !ok {
		return
	}
	if err := m.SetSpawnWorkDir(spawnDir); err != nil && log != nil {
		log.Warn("relay: v2 new_session could not install the recorded workspace",
			"event", "v2.new_session.spawn_dir_install_failed")
	}
}

// activeSessionStarter satisfies relay.SessionStarter by routing an inbound
// new_session frame to the runner bound to the conversation the FRAME NAMES, or —
// when it names none — to the ACTIVE conversation's. It replaced the former
// SessionStarter: w.sup wiring that mis-delivered every new_session to the
// bootstrap supervisor regardless of which conversation the remote client was in
// (the #1121 interrupt shape, applied to new_session), and #2099 closed the
// remaining half of that same defect: the cursor is stamped only by a successful
// route, so pressing New session on a chat before sending anything to it restarted
// whichever chat was last routed. The seams are injected (not raw *Pool/*Registry)
// so the composition test can drive the arms with fakes; production wires
// currentConv: active.CurrentConversation, resolveBound over resolveBoundSession,
// and rotate: pool.RotateForNewSession.
//
// This type is the TRUST BOUNDARY for the client-named id: internal/relay imports
// neither internal/conversations nor internal/sessions, so it hands the string over
// unvalidated and every check lives here.
type activeSessionStarter struct {
	currentConv func() string
	// resolveBound also hands back the conversation's RECORDED workspace — the raw
	// Conversation.Cwd, unvalidated, exactly as ChangeWorkspace stored it (#1475).
	// It is raw on purpose: re-confining it is spawnDirFor's job and must happen at
	// the spawn site, not at the resolve.
	resolveBound      func(convID string) (runner sessions.Runner, oldID sessions.SessionID, recordedCwd string, ok bool)
	rotate            func(oldID sessions.SessionID) (sessions.SessionID, error)
	rotateWithHandoff func(sessions.SessionID, *string) (sessions.SessionID, error)

	// spawnDirFor re-confines a recorded workspace to $HOME at rotation time,
	// answering the directory the successor must spawn in — production wires
	// resolveSpawnDir, the same validator the mint and revive paths use. ("", nil)
	// means no recorded workspace; an error means refused.
	//
	// Optional: nil leaves every successor in the directory its runner already has,
	// which is pre-#1475 behaviour. That tolerance exists for this struct's shape —
	// a constructor-less bag of injected seams built as a named-field literal, where
	// an omitted field is a reachable state — and matches how logger() treats its
	// own nil, degrading rather than panicking on a remotely-driven path.
	spawnDirFor func(recordedCwd string) (string, error)

	// everRan reports whether the conversation's bound session has ever been
	// activated — the durable answer to "has this conversation ever run?" that lets
	// this type tell a conversation created but never messaged, which must stay
	// inert, from a previously-used session that merely has no child right now,
	// which must be resettable (#2521). Production wires Pool.EverActivated, whose
	// doc carries why the reading is exact and where it is blind.
	//
	// Optional: nil answers false for every conversation, which is pre-#2521
	// behaviour — every dormant reset inert. That is the fail-closed direction and
	// it is what keeps every pre-#2521 literal in this package unchanged.
	//
	// IT MUST BE WIRED WHEREVER reviveBound IS. A literal offering the revive
	// without this gate would materialise a never-used conversation's session on an
	// inbound frame, which is the registry mutation AC-3 forbids. The nil default
	// fails closed in the other direction (nothing revives), so the pairing is a
	// wiring discipline rather than an exploitable state.
	everRan func(oldID sessions.SessionID) bool

	// resolveDormant answers a named conversation's PERSISTED binding when
	// resolveBound could not (#2521). It reads the conversation registry and
	// deliberately does not consult the pool: the state it exists for is the one
	// where the pool's answer is "miss", because after a daemon restart
	// sessions.New materialises only the bootstrap and every per-conversation
	// session is a persisted entry with no *Session behind it (#1487).
	//
	// It repeats resolveBound's unknown/unbound refusal rather than inheriting it
	// by proximity — that is the #678 isolation point, and without it the empty
	// CurrentSessionID would reach a revive of the BOOTSTRAP session.
	//
	// Optional: nil leaves the after-restart case exactly as inert as it is today.
	resolveDormant func(convID string) (oldID sessions.SessionID, recordedCwd string, ok bool)

	// reviveBound re-materialises a dormant session into the pool and answers its
	// runner, so the rotation below can proceed against a session Pool.Lookup was
	// missing (#2521). Production wires resolveSpawnDir + Pool.Revive — the same
	// pair sessionRouter.revive uses, so the reset path re-validates the recorded
	// workspace at the spawn site on the same terms the message route already does,
	// rather than trusting a confinement that was checked before the restart.
	//
	// It does NOT spawn claude: Pool.Revive registers the session evicted and the
	// reset tail activates the old identity for wrap-up before rotation.
	//
	// Optional: nil leaves the after-restart case inert.
	reviveBound func(convID string, oldID sessions.SessionID, recordedCwd string) (sessions.Runner, error)

	// reset runs the outgoing session's wrap-up turn and writes its reply as the
	// conversation's handoff note before the rotation (#2477). Optional: nil keeps
	// the synchronous pre-#2477 rotation, which is the shape every test literal in
	// this package still gets for free.
	reset *conversationReset

	// resetting reports the reset's two phases and its falling edge to interactive
	// clients (#2478). Optional on the same terms as reset and every other field in
	// this literal: a nil emitter emits nothing, which is what keeps every
	// pre-#2478 test literal in this package compiling unchanged.
	//
	// It is deliberately SEPARATE from reset rather than a field on it. The
	// coordinator owns one phase of the two and would have to be told when the other
	// began; the tail below is the one place all three edges sit in sequence.
	resetting *resettingEmitterV2

	// log records which arm an inbound new_session took (#2099). Optional: nil
	// falls back to slog.Default() via logger(), mirroring activeInterrupter.
	log *slog.Logger
}

// activeSessionStarter MUST satisfy the LATE seam, not merely the plain one. The
// config field it is assigned to is typed relay.SessionStarter, so only that half
// is compile-checked at the wiring site, and handleNewSession picks the late form
// by a runtime type assertion — meaning a signature that drifted would not fail to
// build, it would silently fall back to the synchronous path and drop #2443's
// reply on every wrap-up. That is precisely how this ticket shipped red once, so
// the assertion is here rather than left to a test to notice (#2477).
var _ relay.LateSessionStarter = activeSessionStarter{}

// resetThenRotate is the reset's tail, run on its own goroutine: the wrap-up turn,
// then the rotation StartNewSession would otherwise have performed inline.
//
// IT IS OFF THE CALLER'S GOROUTINE BECAUSE THE CALLER IS relay's Run LOOP.
// handleNewSession calls StartNewSession inline on the V2SessionManager's single
// Run dispatch goroutine, and a wrap-up is bounded at ninety seconds; blocking
// there would freeze frame dispatch for every connection and every conversation
// the daemon hosts while one background chat wrote a paragraph.
//
// It terminates unconditionally: the wrap-up is bounded by its own deadline and by
// the daemon context, and the rotation below is two map operations and a restart.
//
// THE OUTCOME IS REPORTED WHEN IT BECOMES TRUE, which is the whole reason this
// tail may run at all. #2443's reply is not a status line but a claim with a
// tense: RotatedWithoutWorkspaceError's own doc fixes the pool as re-keyed and the
// session_transition as already broadcast BY THE TIME THE VALUE EXISTS, and then
// forbids it outright for a rotation that failed — "a lie the client cannot
// check". At dispatch time, ninety seconds above this line, that precondition is
// not merely unproven but routinely false: startFreshRunner's own doc calls losing
// the race to a concurrent new_session "the ORDINARY way to land here", and the
// arm below logs it. So the value is minted HERE, under the rotation it describes,
// and handed to outcome — relay.LateSessionStarter's callback, which carries it to
// the Run goroutine where the reply is sealed. A rotation that failed reports the
// plain error and makes no workspace claim, exactly as the synchronous path does.
//
// outcome MAY BE NIL, and that is a reachable state rather than a defensive check:
// a caller holding only the plain SessionStarter method has nowhere to put a late
// answer. The rotation still happens and both outcomes are still recorded; only
// the client reply is absent. The records are written on BOTH paths, not as a
// fallback — they carry conversation_id, which handleNewSession deliberately never
// logs, so they are the daemon-side half of a pair rather than a substitute.
//
// The runner and oldID are the ones resolved at DISPATCH time, which is a wider
// window than the synchronous path's. That is handled where it already was: if a
// rotation won in between, Pool.RotateForNewSession answers ErrSessionNotFound and
// startFreshRunner disarms the #1330 gate and returns it.
//
// IT IS ALSO THE ONE PLACE THE THREE resetting EDGES SIT IN SEQUENCE (#2478), which
// is why they are emitted here rather than inside the coordinator: wrapUp owns one
// phase of the two and knows nothing of the rotation that is the other.
//
// THE FALLING EDGE IS DEFERRED, AND ITS POSITION RELATIVE TO release IS
// LOAD-BEARING. Registered below `defer release()`, LIFO runs it FIRST — so the
// conversation is still claimed when active:false goes out. Reversed, release would
// admit a second reset whose own wrapping_up could reach a client AHEAD of this
// one's falling edge, and a client that cleared its indicator on the late arrival
// would strand the second reset's — the one ordering error the "every rising
// sequence ends in a falling edge" guarantee cannot recover from. Deferring also
// puts it on all three exits at once, which is the argument reportNewSessionOutcome
// records for itself: the fourth edit would forget one.
func (a activeSessionStarter) resetThenRotate(release func(), outcome func(error),
	runner sessions.Runner, oldID sessions.SessionID, convID, spawnDir string, refused bool) {
	defer release()
	a.resetting.wrappingUp(convID)
	// The bool is an OUTCOME, not an error: a wrap-up that produced no note does not
	// fail the reset, and nothing below branches on it. It exists so the phase change
	// can say whether the successor starts with a note.
	wroteNote := a.reset.wrapUp(convID)
	a.resetting.restarting(convID, wroteNote)
	defer a.resetting.done(convID)
	rotate := a.rotate
	if a.rotateWithHandoff != nil {
		outcome := "skipped"
		if wroteNote {
			outcome = "written"
		}
		rotate = func(id sessions.SessionID) (sessions.SessionID, error) { return a.rotateWithHandoff(id, &outcome) }
	}
	if err := startFreshRunner(runner, oldID, spawnDir, rotate, a.log); err != nil {
		a.logger().Warn("relay: v2 new_session could not rotate after the wrap-up turn",
			"event", "v2.new_session.rotate_failed",
			"conversation_id", convID,
			"err", err)
		reportNewSessionOutcome(outcome, err)
		return
	}
	if refused {
		// Reached only with the rotation returned nil, which is the tense the reply
		// claims. No path: resolveSpawnDir's confinement error is not carried here,
		// matching the posture that function keeps for its own record.
		a.logger().Warn("relay: v2 new_session rotated without the conversation's recorded workspace",
			"event", "v2.new_session.workspace_refused",
			"conversation_id", convID)
		reportNewSessionOutcome(outcome, &relay.RotatedWithoutWorkspaceError{ConversationID: convID})
		return
	}
	reportNewSessionOutcome(outcome, nil)
}

// reportNewSessionOutcome delivers one new_session outcome to a
// relay.LateSessionStarter callback, tolerating the nil callback a plain
// SessionStarter caller leaves behind (#2477). A free function rather than a
// method because it reads nothing from the starter, and one place rather than a
// nil check at each of resetThenRotate's three exits, where the fourth edit would
// forget it.
func reportNewSessionOutcome(outcome func(error), err error) {
	if outcome == nil {
		return
	}
	outcome(err)
}

// logger returns a's logger, falling back to slog.Default() when unset.
// activeSessionStarter is a constructor-less bag of injected seams built as a
// named-field literal, so an omitted field is a reachable state — and a nil
// *slog.Logger panics on first use, which on this remotely-driven relay path
// would be a latent crash on a rarely-hit inert arm. Falling back to the default
// logger (not a discard handler) keeps an unwired literal's record visible.
func (a activeSessionStarter) logger() *slog.Logger {
	if a.log == nil {
		return slog.Default()
	}
	return a.log
}

// StartNewSession starts a fresh session in conversationID's bound runner, or in
// the active conversation's when conversationID is empty.
//
// THE ORDER OF THE FIRST TWO BRANCHES IS THE DESIGN. The empty-string check comes
// BEFORE the shape check because conversations.ValidID returns false for the empty
// string (its own doc says so): reversed, every un-upgraded client's bare frame —
// the shape new_session had from #831 until #2099 — would fail validation and go
// inert, silently breaking the compatibility this ticket promises.
//
// After that, order mirrors activeInterrupter.SendEsc and every ambiguous state is
// inert (nil, no rotation, no respawn, no reply, and never a fall-through to the
// bootstrap session): no conversation to act on → inert; a named id that is not a
// canonical conversation id → inert without ever reaching the registry;
// unbound/dangling binding → inert, resolveBound's CurrentSessionID == "" guard
// being the #678 isolation enforcement; a runner that cannot restart → inert; a
// NAMED conversation with no live child → inert. Only a rotate error propagates,
// for handleNewSession to Warn-log and tolerate (best-effort contract).
//
// THAT LAST ARM IS NAMED-ONLY, and the asymmetry is deliberate rather than an
// oversight. AC-4 requires a named conversation with no live child to be inert, and
// AC-3 requires the bare frame to behave EXACTLY as before #2099 — where an evicted
// cursor conversation rotates and comes back up under the fresh id. Applying the
// liveness guard to both would satisfy one at the cost of the other, so it is
// applied to the path this ticket introduces and withheld from the path it promises
// not to disturb.
//
// Every arm records the id it refused, at DEBUG because the id is client-supplied —
// the posture handleDequeueMessage already takes for its own client-named
// conversation_id, and deliberately NOT activeInterrupter's Info, which records a
// daemon-authored arm identity rather than a remote string. The two registry
// refusals share ONE record: resolveBoundSession refuses the unknown and the
// unbound identically, so this caller structurally cannot distinguish them, which
// is also what keeps the frame from answering "does this conversation exist?".
//
// The cursor is never WRITTEN here; only sessionRouter.Route stamps it. Rotating a
// named conversation therefore leaves the active conversation where it was.
//
// It is relay.SessionStarter's method and answers only what it can prove: the
// wrap-up arm's rotation outlives this call, so this form returns nil there. The
// answer is not lost — StartNewSessionLate is the form that receives it (#2477).
func (a activeSessionStarter) StartNewSession(conversationID string) error {
	err, _ := a.start(conversationID, nil)
	return err
}

// StartNewSessionLate is relay.LateSessionStarter's method: the same arms, with
// the wrap-up arm's outcome delivered through outcome once its rotation has
// actually happened (#2477). handleNewSession asserts this method on the seam and
// prefers it, so in the daemon it is the form that runs.
//
// outcome IS CALLED EXACTLY ONCE on every arm. Synchronously here for every arm
// that resolves inline — including the inert ones, which report nil and elicit no
// reply — and from resetThenRotate's goroutine for the one that defers. The split
// is `deferred`, answered by start itself, so neither this function nor a future
// arm has to infer "did that one defer?" from the error being nil.
func (a activeSessionStarter) StartNewSessionLate(conversationID string, outcome func(error)) {
	if err, deferred := a.start(conversationID, outcome); !deferred {
		reportNewSessionOutcome(outcome, err)
	}
}

// start holds every arm of both seam forms, so the two cannot drift (#2477).
//
// It answers (err, deferred): err is what the synchronous forms return, and
// deferred says the outcome has been handed to the wrap-up tail and will arrive
// through outcome instead. The pair is exhaustive — deferred == true always comes
// with a nil err, and a caller that ignores deferred gets exactly the pre-#2477
// synchronous contract, which is what StartNewSession relies on.
//
// outcome is carried rather than consumed: only the wrap-up arm uses it, and a nil
// one there is the unwired-caller posture, in which the rotation still runs
// asynchronously and only the client reply is absent.
func (a activeSessionStarter) start(conversationID string, outcome func(error)) (err error, deferred bool) {
	convID := conversationID
	named := conversationID != ""
	switch {
	case convID == "":
		// Nothing named: the pre-#2099 path, verbatim. The cursor's own id is
		// daemon-authored and is deliberately NOT shape-checked — doing so would
		// change behaviour on the path this branch exists to preserve.
		convID = a.currentConv()
		if convID == "" {
			// No id to carry — the record's information is its existence: the frame
			// reached here and nothing was active.
			a.logger().Debug("relay: v2 new_session inert; no active conversation",
				"event", "v2.new_session.no_active_conv")
			return nil, false
		}
	case !conversations.ValidID(convID):
		a.logger().Debug("relay: v2 new_session inert; named conversation id is not canonical",
			"event", "v2.new_session.invalid_conv_id",
			"conversation_id", boundedConvID(convID))
		return nil, false
	}
	var release func()
	if a.reset != nil {
		var armed bool
		release, armed = a.reset.begin(convID)
		if !armed {
			a.logger().Debug("relay: v2 new_session inert; a reset is already in progress",
				"event", "v2.new_session.reset_in_progress",
				"conversation_id", convID)
			return nil, false
		}
		defer func() {
			if release != nil {
				release()
			}
		}()
	}

	runner, oldID, recordedCwd, ok := a.resolveBound(convID)
	// used carries the dormant arm's PROOF forward rather than re-deriving it: a
	// revived session reached this line only because everRan already answered true
	// for it, and the liveness guard below would otherwise ask the same question a
	// second time for an answer it cannot have changed.
	used := false
	if !ok {
		// #2521: "the pool has no session for it" is not the same as "there is
		// nothing to reset". After a daemon restart sessions.New materialises only
		// the bootstrap, so a previously-used conversation's session is a persisted
		// entry Pool.Lookup misses — the state the message route already recovers
		// from through sessionRouter.revive and this verb did not. reviveDormantBound
		// writes its own record on every refusal, including the unknown/unbound one
		// this arm used to write here.
		runner, oldID, recordedCwd, ok = a.reviveDormantBound(convID)
		if !ok {
			return nil, false
		}
		used = true
	}
	// AC-4's fourth row — which was first shipped broken, when a capability probe for
	// RestartFresh stood in for it. What the runner CAN do is not the question: every
	// runner can, since RestartFresh is on sessions.Runner (#2592), and the runner is
	// assigned at mint time. A conversation created but never messaged therefore
	// reaches this line with a real runner whose child has never spawned (#2085
	// defers the spawn to the first message), and rotating it would rekey the pool,
	// persist sessions.json, rebind the conversation and broadcast a
	// session_transition telling every client to render a delimiter for a chat that
	// has never had a turn. RestartFresh itself would spawn nothing, so the rotation
	// would be pure observable damage with no session to show for it.
	//
	// State().ChildPID is the liveness signal rather than Phase because it is the one
	// the field's own doc defines that way ("PID of the running child, or 0 when
	// none") — an unstarted, backing-off, evicted or stopped runner all report 0, and
	// all four are states where there is nothing to restart fresh. Racing a spawn
	// that has started but not yet published its pid reads 0 too and refuses; that is
	// the same fail-safe direction the whole reject set takes, and the frame is
	// re-sendable.
	//
	// SINCE #2521 THE ZERO PID IS NO LONGER THE WHOLE ANSWER. It covers an unstarted
	// runner AND one whose child has stopped, backed off or been evicted, and only
	// the first of those has nothing to reset — the rest are the channel an operator
	// comes back to after lunch, whose Reset did nothing at all. everRan splits the
	// two on the durable signal (Pool.EverActivated), so the refusal keeps its
	// original subject — a conversation created but never messaged — and loses the
	// three states it was over-reaching into. The guard is still fail-safe in the
	// same direction: a nil seam, an unknown id, or a spawn racing its own pid all
	// read "never ran" and refuse, and the frame is re-sendable.
	//
	// ONE CONSEQUENCE IS KEPT RATHER THAN CORRECTED: a REPEATED Reset now rotates
	// every time. Pool.rekeyLocked stamps lastActiveAt while createdAt is immutable,
	// so the successor of a rotation reads as previously used and a second frame
	// arriving before any message re-keys again. Refusing it would need a signal
	// these timestamps cannot carry — "has a child ever spawned under the CURRENT
	// id" — because the Session survives the re-key; only the runner's rotatePending
	// latch knows, and exposing it is a new API for a state no AC names. The cost is
	// one extra separator for a session that has had no turn, it is operator-driven
	// rather than client-replayable, and it is arguably what pressing the control
	// twice asks for.
	live := runner.State().ChildPID != 0
	if named && !live && !used && !a.hasEverRun(oldID) {
		a.logger().Debug("relay: v2 new_session inert; named conversation has no live child",
			"event", "v2.new_session.no_live_child",
			"conversation_id", convID)
		return nil, false
	}
	// Used dormant conversations need the same handoff as live ones. The tail
	// resumes the old identity before writing, under the shared wrap-up deadline.
	// Never-used named conversations have already been refused above.
	if a.reset != nil && (live || used || a.hasEverRun(oldID)) {
		// Resolved SYNCHRONOUSLY so it stays BELOW every inert arm AND below the guard
		// above, which is what keeps a repeated frame from driving MkdirAll — the
		// containment resolveSpawnDir's own doc requires. The refusal it answers
		// travels WITH the tail rather than being answered from here; resetThenRotate
		// mints #2443's value under the rotation that makes it true.
		spawnDir, refused := a.resolveSpawnDir(convID, recordedCwd)
		tailRelease := release
		release = nil
		go a.resetThenRotate(tailRelease, outcome, runner, oldID, convID, spawnDir, refused)
		// DEFERRED, AND THE ONLY ARM THAT IS: this frame owes the client a reply it
		// cannot yet make true. The only reply this verb has is #2443's, and that value
		// asserts a COMPLETED rotation — one that is ninety seconds away here and may
		// not happen at all. Answering now would be the "lie the client cannot check"
		// its own doc forbids, so the answer travels with the tail and arrives when it
		// is true. A caller with no late channel (outcome == nil) simply does not get
		// it; the rotation is unaffected.
		return nil, true
	}
	spawnDir, refused := a.resolveSpawnDir(convID, recordedCwd)
	if err := startFreshRunner(runner, oldID, spawnDir, a.rotate, a.log); err != nil {
		// The rotation did not happen, so the refusal is not reportable: "rotated
		// without the workspace" would describe a rotation the client's own
		// session_transition never announced. The rotate error is the pre-#2443
		// answer and stays the answer; resolveSpawnDir's record still stands.
		return err, false
	}
	if refused {
		// The rotation COMPLETED and only the move did not (#2443). Not a failure —
		// the type's own doc block says so — and the only outcome handleNewSession
		// answers the client about. convID is resolved: the frame's id after
		// conversations.ValidID, or the cursor's own, so it is bounded and canonical
		// on both paths and is never the raw client string.
		return &relay.RotatedWithoutWorkspaceError{ConversationID: convID}, false
	}
	return nil, false
}

// resolveSpawnDir re-confines the conversation's recorded workspace to $HOME and
// answers the directory the successor must spawn in, or "" to leave the runner
// where it is (#1475).
//
// IT RE-VALIDATES RATHER THAN TRUSTING, taking sessionRouter.revive's posture and
// explicitly not sessionMinter.Create's. Create may defer without re-validating
// because resolveSpawnDir's realpath is frozen onto the session at build time, so
// no phone-influenced state is re-read between the check and the chdir. Nothing is
// frozen here: recordedCwd is raw persisted bytes in a mutable file, written by
// change_workspace at an arbitrary earlier moment and possibly across a daemon
// restart, which is revive's situation exactly — "a path valid then can be turned
// into an escape before the restart, and this is the spawn site that would
// otherwise believe the stale check". So the validator runs again, here, on every
// rotation.
//
// FAIL-CLOSED MEANS KEEPING THE OLD DIRECTORY, never spawning in an unconfined
// one: both an empty recording and a refusal answer "", and the rotation still
// completes with the child still coming up where it was. The two differ in the
// SECOND return and in whether a record is written — an unset workspace is not a
// refusal.
//
// THE BOOL IS THE SAME CONDITION THE RECORD REPORTS, deliberately so the wire and
// the log cannot disagree: refused is true on exactly the arm that writes
// spawn_dir_rejected, and on no other. It carries no text at all, which is what
// keeps the confinement error's path out of everything downstream — the caller
// turns it into a relay.RotatedWithoutWorkspaceError naming the conversation and
// nothing else (#2443). installSpawnDir's later failure is a DIFFERENT condition
// with its own record and is deliberately not reported here: by then the
// directory was accepted and the rotation is already committed.
//
// IT IS CALLED BELOW EVERY INERT ARM, which is load-bearing rather than tidy:
// resolveSpawnDir creates a directory and writes ~/.claude.json, so hoisting it
// would let a frame naming a conversation with no live child drive MkdirAll on the
// daemon's behalf. It also blocks in syscalls on V2SessionManager's single Run
// dispatch goroutine — the honest consequence handlers.createConversationMintTimeout
// already records for the neighbouring mint path, and deliberately NOT defended
// with a deadline, which cannot interrupt a syscall and would claim a protection it
// does not provide. Only a frame that will actually rotate pays it.
//
// SECURITY: the wrapped error names the resolved path and the $HOME boundary and
// MUST NOT be logged. Pool.Revive's contract is that a phone-influenced workspace
// path must not reach a log, and sessionTranscriptDir's SECURITY paragraph closes
// the same channel (#833). The record carries the event and the conversation id —
// which is already logged unbounded on the resolvable arms above — and nothing
// else. At Warn rather than the arms' Debug: this one is about the daemon's own
// stored state failing its own validator, not about a string a client just sent.
func (a activeSessionStarter) resolveSpawnDir(convID, recordedCwd string) (dir string, refused bool) {
	if a.spawnDirFor == nil || recordedCwd == "" {
		return "", false
	}
	dir, err := a.spawnDirFor(recordedCwd)
	if err != nil {
		a.logger().Warn("relay: v2 new_session rejected the recorded workspace; keeping the current one",
			"event", "v2.new_session.spawn_dir_rejected",
			"conversation_id", convID)
		return "", true
	}
	return dir, false
}

// maxLoggedConvID bounds how much of a REFUSED conversation id reaches a log
// record. Only the invalid-shape arm needs it: every other arm logs a string that
// has already passed conversations.ValidID, so it is provably 36 bytes. That arm is
// the one place an arbitrary client-chosen string reaches a log call, and it is
// bounded upstream only by the application-envelope cap — kilobytes per frame, and
// a paired client may send frames freely. 64 is generously above a canonical id's
// 36 so a near-miss (a stray character, a wrong-cased id) still prints whole and
// stays diagnosable.
const maxLoggedConvID = 64

// boundedConvID renders an untrusted conversation id for a log record: unchanged
// when it is already short, and otherwise a COPIED prefix carrying an elision
// marker. The copy is load-bearing — a bare s[:n] would share the decoded frame's
// backing array, so a buffered log record would pin the whole payload allocation.
// The marker matters too: without it a truncated id reads as a complete one, and a
// reader would chase a conversation that was never named.
func boundedConvID(s string) string {
	if len(s) <= maxLoggedConvID {
		return s
	}
	return strings.Clone(s[:maxLoggedConvID]) + "…(truncated)"
}

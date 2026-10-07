package sessions

import (
	"context"

	"github.com/pyrycode/pyrycode/internal/modelfamily"
)

// UpdateSettings merges the fields marked present in update into the stored
// settings of session id and re-persists, taking p.mu (write) exactly as Rename
// does for label. A nil field in update leaves the stored value untouched; a
// non-nil field overwrites it (including "" for Model/Effort and false for
// YOLO). An absent (nil) YOLO can never enable bypass — only an explicit
// non-nil *true does. Returns ErrSessionNotFound for an unknown id, creating no
// entry. A no-op update (every present field already equal to the stored value,
// or every field nil) writes nothing to disk, keeping the registry mtime
// stable. On a saveLocked failure the in-memory settings are rolled back so
// memory stays consistent with disk.
//
// The permission posture is stored as TWO fields that can never disagree (#2043).
// validatePermissionUpdate rejects an unrecognised mode
// (ErrUnsupportedPermissionMode) and a mode contradicting a YOLO in the same frame
// (ErrPermissionModeConflict) before anything is mutated; what survives derives
// both fields together:
//
//	update carries                          stored mode        stored YOLO
//	yolo:true                               bypassPermissions  true
//	yolo:false, stored mode is bypass       default            false
//	yolo:false, stored mode is anything else unchanged         false
//	mode bypassPermissions                  bypassPermissions  true
//	any other known mode                    that mode          false
//
// Row three is the one to read twice: a yolo:false must not drag an operator out
// of plan and into default as a side effect of naming a field it was not asked
// about.
//
// After a successful persist of a real change the change is also LIVE-APPLIED to
// the session's supervisor, by one of two mechanisms picked on what the update
// carried — which fields, and for the posture its value too (inBandDeliverable):
//
//   - A change claude accepts on the stream the daemon already holds open is
//     delivered IN-BAND (#1581, #1604, #2043, #2066, #2280): model through
//     set_model, effort through an ordinary /effort user turn, and ANY of the six storable postures as a
//     set_permission_mode control request, so the child is neither terminated nor
//     respawned and its transcript survives. That branch still installs the
//     recomposed argv, via SetSpawnArgs — Restart's swap half — because skipping
//     the install would let the operator's change silently revert on the next
//     crash-respawn or evict → Activate. Fire-and-forget; see
//     deliverSettingsInBand.
//   - Every other change keeps the live restart (#842): the argv is recomposed
//     from the persisted settings and, if a child is running, that child is
//     killed so the supervisor relaunches it — resuming the conversation — under
//     the new settings. Since #2066 this is the path a model or effort cleared back
//     to claude's own default takes, and nothing else.
//
// The posture split is on the POSTURE ASKED FOR, not on presence, and it is
// measured rather than assumed. #1595 and #2041 measured the five non-escalating
// modes on a held-open stream; #2060 measured the ESCALATION the same way, on a
// child launched with --dangerously-skip-permissions, which #2065 made every child.
// So all six now apply to the running child with no respawn. Before #2066 the
// escalation was the exception, because claude refused it in words on a child
// launched without the flag and only a relaunch under a recomposed argv could grant
// it; that asymmetry is what this ticket removed, and every caller that modelled it
// can stop.
//
// For an evicted session neither mechanism finds a child; the argv install alone
// applies on the next Activate, and the caller still sees success. Both branches
// run OUTSIDE p.mu (every installer is non-blocking and drives only
// supervisor-internal state, so the Pool.mu → Session.lcMu order is untouched)
// and only on a real change: an unknown id, a no-op update, and a persist
// failure all skip them, so a still-correct running child is never disturbed.
//
// Lock order: release p.mu after persistence, then take sess.spawnArgsMu before
// re-reading prompt suppression under p.mu. Keep spawnArgsMu through either argv
// install, with p.mu released before runner calls, and release it before in-band
// delivery. Does not take Session.lcMu; spawnBase and sup are immutable.
func (p *Pool) UpdateSettings(id SessionID, update SettingsUpdate) error {
	p.mu.Lock()
	sess, ok := p.sessions[id]
	if !ok {
		p.mu.Unlock()
		return ErrSessionNotFound
	}
	if err := validatePermissionUpdate(update); err != nil {
		p.mu.Unlock()
		return err
	}
	merged := sess.settings
	if update.Model != nil {
		// Stored as its family, as canonicalSettings stores every read: the menu
		// offers one row per family, and the session follows that family.
		merged.Model = modelfamily.Alias(*update.Model)
	}
	if update.Effort != nil {
		merged.Effort = *update.Effort
	}
	// The posture's two fields move together, and the mode arm subsumes the YOLO
	// arm: validatePermissionUpdate has already refused a frame carrying both
	// with values that disagree, so when both are present they derive the same
	// pair and running the mode arm alone is not a precedence rule.
	switch {
	case update.PermissionMode != nil:
		merged.PermissionMode = *update.PermissionMode
		merged.YOLO = merged.PermissionMode == permissionModeBypass
	case update.YOLO != nil:
		merged.YOLO = *update.YOLO
		// canonicalPermissionMode is what makes a revoke land in default while
		// leaving a non-bypass mode alone: a yolo:false must not drag an
		// operator out of plan as a side effect of naming a field it was not
		// asked about.
		merged.PermissionMode = canonicalPermissionMode(merged.PermissionMode, merged.YOLO)
	}
	if merged == sess.settings {
		p.mu.Unlock()
		return nil
	}
	prev := sess.settings
	sess.settings = merged
	if err := p.saveLocked(); err != nil {
		sess.settings = prev
		p.mu.Unlock()
		return err
	}
	// Capture the immutable runner and release p.mu before any runner calls.
	// Compose argv only inside the publication lock below: a prompt refresh may
	// suppress its file while the posture install is in flight.
	sup := sess.sup
	p.mu.Unlock()

	// Both branches install newArgs; only one kills. Swap BEFORE the write: the
	// install is the durable half and is non-blocking, so if the delivery fails
	// the next spawn still carries the change.
	//
	// The posture install (#2064) sits ABOVE the split because it is the one thing
	// BOTH branches need and only one of them would otherwise get. The runner asserts
	// its stored posture to every child it spawns, and neither branch rebuilds the
	// runner — so a construction-time value goes stale here, and a later
	// crash-respawn would re-assert the posture the session had at daemon start. The
	// escalation branch below is the half that makes the placement necessary rather
	// than tidy: it never calls SetPermissionMode, so an install folded into
	// deliverSettingsInBand would miss exactly the transition that most needs it.
	//
	// Unconditional rather than gated on update.PermissionMode/YOLO being present:
	// merged.PermissionMode is the session's stored posture whatever this update
	// named, so re-installing it is idempotent and a future branch cannot forget it.
	// It is non-blocking and takes no Pool lock, so it is safe here, past the unlock.
	sup.SetSpawnPermissionMode(merged.PermissionMode)
	sess.spawnArgsMu.Lock()
	p.mu.RLock()
	newArgs := sess.spawnArgs(merged)
	p.mu.RUnlock()
	if inBandDeliverable(update) {
		sup.SetSpawnArgs(newArgs)
		sess.spawnArgsMu.Unlock()
		p.deliverSettingsInBand(id, sup, update, merged)
		return nil
	}
	// This is a LIVE PRODUCTION CALLER of Restart, and #2066 did not remove it —
	// though it did remove the reason the previous revision gave. The bypass posture
	// has now moved off this branch ENTIRELY: both spellings of an enable go in-band
	// above, so the escalation reaches Restart only in combination with something
	// else. What keeps this call reachable is the OTHER half of inBandDeliverable's
	// reject set: a Model or Effort explicitly cleared to "" means "run at claude's
	// own default", which claudeSettingsArgs expresses by omitting the flag. Pyrycode
	// deliberately retains restart semantics for an explicit clear, so it is applied by relaunching
	// under the recomposed argv. #1574 may still NOT delete Restart, and a future
	// slice that gives the empty value an in-band form is the one that inherits the
	// question.
	// Refuse writes before the kill (#1513). Inside this branch and NOT above the
	// split: the in-band branch tears no child down, so an arm hoisted above it would
	// refuse turns for every live settings change until the next respawn. It is
	// non-blocking, takes no Pool lock and makes no call-out, so it is safe here past
	// the unlock beside the two SetSpawn* calls, and it cannot delay the Restart.
	sup.BeginTeardown()
	sup.Restart(newArgs)
	sess.spawnArgsMu.Unlock()
	return nil
}

// UpdateDormantSettings merges update's Model and Effort into id's dormant
// registry entry and persists it, or returns ErrSessionNotFound if this pool
// holds no dormant entry under that id. It is UpdateSettings' dormant half: the
// two partition the ids the daemon has a record of, on the same basis
// DormantSettingsFor and SettingsFor partition them.
//
// It exists so set_session_settings can land on a conversation this daemon has
// not materialised (#2463). Pool.New materialises only the bootstrap, so after a
// restart that is EVERY conversation until its first message revives it, and a
// model or effort chosen before that message was refused with session.not_found
// while the menus the client had just rendered said otherwise (#2449 fixed the
// read and made this reachable).
//
// A SECOND WRITE rather than a fallback folded into UpdateSettings, for the
// reason DormantSettingsFor records one layer up and one more of its own.
// Folding it in would change what ErrSessionNotFound means for every other caller
// of the live write, several of which use exactly that answer to decide a session
// is not addressable. And UpdateSettings' body past its persist is entirely about
// a live session — the recomposed argv, the posture install, the in-band
// delivery, the supervisor capture — none of which has a dormant analogue.
//
// IT MATERIALISES NOTHING, deliberately, and that is a contract rather than an
// implementation detail. #2449 AC 3 made the settings READ materialise nothing so
// that N channel activations cannot become N sessions; the write rides the same
// frame family on the same restart edge, and a client that re-asserts its footer
// state on activation would otherwise wake every channel it touched. Reviving
// here is also not available on its own terms: Pool.Revive needs a spawn
// directory to re-validate through the caller's $HOME confinement, this seam is
// keyed by SESSION rather than by conversation, and a session no conversation
// binds has no Cwd at all.
//
// THE POSTURE IS REFUSED, NOT PERSISTED, and the refusal is honesty rather than
// caution. Both readers of p.dormant build the posture structurally from Model
// and Effort alone — revivedSettings never looks at the entry's yolo or
// permission_mode, and DormantSettingsFor derives the default from the same two
// fields — so a persisted posture is invisible to both, and accepting one would
// report success for a change the very next read contradicts. Making it visible
// instead would mean teaching the revive to read a persisted posture back, and
// the disk cannot tell a posture granted after a restart from one granted before
// it, so that would resurrect exactly the bypass a restart revokes (#1487, ADR
// 035 as amended by #2448). Carrying a NON-ESCALATING mode across a revive is a
// real option and a security decision of its own; it is not this method's.
//
// The exclusion is doubled rather than singled: the refusal below returns before
// any mutation, AND the merge names Model and Effort literally, so a posture
// could not reach the entry even if the guard were deleted. That is mintSettings'
// and revivedSettings' recorded discipline — a field added to registryEntry later
// is not carried until someone opts it in — applied to a write.
//
// VALIDATING THE VALUES IS NOT THIS METHOD'S JOB, exactly as it is not
// UpdateSettings'. It operates on operator-trusted input. The relay handler owns
// the charset and length shape check for Model and the closed enum for Effort,
// and for a non-empty model cmd/pyry's settingsUpdaterAdapter then owns the
// membership check against the retained published vocabulary before this method
// can write anything. A written Model becomes the revived child's --model, so a
// caller that skips that gate is handing an unvalidated value to an argv sink.
//
// Carried over from UpdateSettings' shape: a no-op returns success without
// rewriting the registry (compared on the two scalar fields rather than on the
// whole entry, which embeds time.Time values whose == compares representation
// rather than instant), and a failed save restores the previous entry.
//
// Concurrency: MUST be called with p.mu unheld — one Lock acquisition, no
// delegation to another locking accessor, UpdateSettings' contract verbatim. The
// lookup, the merge and the save all run inside that one critical section, so
// there is no check-then-mutate gap of this method's own. Never takes
// Session.lcMu; p.dormant is a p.mu-guarded field like sessions and label.
//
// ONE WINDOW OUTSIDE IT, bounded by p.dormant only ever shrinking: a caller
// composing this after a live write (settingsUpdaterAdapter) can have a revive
// land between the two. The id can only move live-ward, so this method finds a
// CLEAN MISS rather than a torn entry, and answers ErrSessionNotFound. That
// refusal is correct rather than merely safe: the settings were applied to
// nothing. No retry is attempted — the operator's next pick reaches the now-live
// session through the live write.
//
// The SECOND window this doc used to name is closed (#2492). Pool.Revive
// evaluated revivedSettings as an ARGUMENT to materialise, so that read's RLock
// was released before materialise took the write lock and retired the entry, and
// a write landing in the gap was acknowledged and then dropped — the session
// materialised under the value read before it. materialise now takes its settings
// as a source and evaluates it inside the critical section that retires the
// entry, so a write reaching this method before that section is CARRIED by the
// revived session and one reaching it after gets ErrSessionNotFound above. Those
// were always the two outcomes this seam's docstrings reasoned about; what is
// gone is the third.
//
// No id validation and no logging, DormantSettingsFor's posture verbatim: the id
// names no file and never leaves the map lookup, so a malformed id is a map miss
// — already the correct answer — and both errors are returned bare rather than
// wrapped with it, so a hostile id cannot be reflected into a log line or a wire
// frame a consumer builds from the error.
func (p *Pool) UpdateDormantSettings(id SessionID, update SettingsUpdate) error {
	p.mu.Lock()
	entry, ok := p.dormant[id]
	if !ok {
		p.mu.Unlock()
		return ErrSessionNotFound
	}
	if update.YOLO != nil || update.PermissionMode != nil {
		p.mu.Unlock()
		return ErrDormantPostureUnsupported
	}
	model, effort := entry.Model, entry.Effort
	if update.Model != nil {
		model = modelfamily.Alias(*update.Model)
	}
	if update.Effort != nil {
		effort = *update.Effort
	}
	if model == entry.Model && effort == entry.Effort {
		p.mu.Unlock()
		return nil
	}
	prev := entry
	entry.Model, entry.Effort = model, effort
	p.dormant[id] = entry
	if err := p.saveLocked(); err != nil {
		p.dormant[id] = prev
		p.mu.Unlock()
		return err
	}
	p.mu.Unlock()
	return nil
}

// validatePermissionUpdate refuses the two posture updates that have no correct
// reading, BEFORE Pool.UpdateSettings mutates anything, so a rejected frame
// leaves the stored settings, the registry file and the running child
// byte-identical to their prior state:
//
//   - a mode outside permissionModeKnown, including the empty string — unlike
//     Model and Effort, where "" means "omit the flag, run at claude's own
//     default", the default posture is a NAMEABLE mode, so an explicit "" has no
//     reading. Refusing here is what keeps an unrecognised value out of the
//     registry, and therefore out of every argv composed from it later and away
//     from the child.
//   - a mode and a YOLO bit that contradict each other. Neither precedence is
//     fail-safe in both directions, so neither is chosen; see
//     ErrPermissionModeConflict.
//
// Neither error carries the rejected value (#833). Total over any
// SettingsUpdate: an update naming no mode is trivially valid, so the caller can
// invoke it unconditionally.
func validatePermissionUpdate(update SettingsUpdate) error {
	if update.PermissionMode == nil {
		return nil
	}
	mode := *update.PermissionMode
	if !permissionModeKnown(mode) {
		return ErrUnsupportedPermissionMode
	}
	if update.YOLO != nil && (mode == permissionModeBypass) != *update.YOLO {
		return ErrPermissionModeConflict
	}
	return nil
}

// inBandDeliverable reports whether update's PRESENT fields are all changes
// claude accepts on a stream it is already reading — a non-empty Model through a
// set_model control request, a non-empty Effort as an /effort command, and any of the SIX storable postures —
// the five non-escalating ones and, since #2066, the escalation — as a
// set_permission_mode control request (#1604, #2043, #2066) — and so the changes
// Pool.UpdateSettings can live-apply without tearing the child down.
//
// The rule keys on what the wire carried, never on merged-vs-previous per field:
// SetSessionSettingsPayload's fields are omitempty pointers documented as a
// presence contract, so a client changing only the model sends only the model.
// A present YOLO or PermissionMode is read for its VALUE as well as its presence,
// which is still a property of the frame and NOT a diff against stored state — do
// not quietly convert this predicate into a per-field differ. What #2043 adds is
// that one posture is expressed by TWO fields, so an update naming either is an
// update to the posture; the routing still reads the frame, and it is
// deliverSettingsInBand that resolves which posture RESULTS. Three consequences
// are deliberate rather than incidental:
//
//   - The split is on the POSTURE ASKED FOR, not on the presence of a posture
//     field (#1595 and #2041 measured the five live, #2060 the escalation). It no
//     longer splits on DIRECTION, which is #2066's change: all six postures go in
//     band, an escalation included, whether spelled as yolo:true or as the
//     bypassPermissions mode. Claude used to gate the escalation on the launch argv
//     and refuse the control request in words, so only a respawn under the
//     recomposed argv could grant it; #2065 put that flag on every argv and #2060
//     measured claude accepting the re-escalation on such a child at 2.1.239. Both
//     spellings had to open together — internal/relay's validPermissionMode refuses
//     the mode string, so a mobile client can only ever send the bit, and opening
//     the mode clause alone would have shipped the change as a no-op for every
//     relay session.
//   - A revoke goes in-band INCLUDING when it equals the stored value, which
//     sends a revocation to a child that was never in bypass. Harmless and
//     deliberately not fixed: the delivery is fire-and-forget, the installed argv
//     is the durable half and composes the same non-escalated posture either way,
//     and the child is not in bypass to begin with. (Before #2065 that read "and
//     carries no bypass flag either way"; every argv carries the flag now, and it
//     is the --permission-mode pair beside it, plus the spawn-time write, that
//     carry the revocation. The redundancy is unchanged; only its spelling is.)
//     It is the same redundancy this path already
//     tolerates for an unchanged model re-sent alongside a new effort.
//   - A present-but-empty Model or Effort takes the restart. Empty means "run at
//     claude's own default", which claudeSettingsArgs expresses by OMITTING the
//     flag; this contract does not use control-layer reset spellings. That reject wins over
//     ANY posture change in the same frame — an escalation as much as a revoke —
//     and loses nothing, since the restart recomposes argv from the merged
//     settings, so the respawn carries the posture. No frame can lose a posture
//     change by mixing. Since #2066 these two clauses are also the ONLY surviving
//     route from an escalation to the restart branch, which is what keeps
//     Pool.UpdateSettings' Restart call reachable at all.
//
// Total over any SettingsUpdate. The nothing-present clause is redundant at the
// one call site, since an all-nil update returns early as a no-op before the
// live-apply, but keeping it makes the predicate independently testable instead
// of dependent on a caller-side invariant.
func inBandDeliverable(update SettingsUpdate) bool {
	// Refused by NON-MEMBERSHIP, so no spelling is named here and every
	// unanticipated one is refused for free — the shape the writer-side allow-list
	// uses for the same reason. permissionModeKnown rather than permissionModeInBand
	// is #2066's one-word routing open: every posture this daemon can STORE is now
	// one it can deliver in band, so the predicate that answers "storable" answers
	// this too. permissionModeInBand is deliberately NOT widened — three other
	// callers read it for a different question and must keep excluding the
	// escalation; see its own doc. An unrecognised mode never reaches this predicate
	// in production (validatePermissionUpdate rejects the frame outright), so this
	// arm is defence rather than a live route.
	if update.PermissionMode != nil && !permissionModeKnown(*update.PermissionMode) {
		return false
	}
	if update.Model == nil && update.Effort == nil && update.YOLO == nil && update.PermissionMode == nil {
		return false
	}
	if update.Model != nil && *update.Model == "" {
		return false
	}
	if update.Effort != nil && *update.Effort == "" {
		return false
	}
	return true
}

// effortSetter is a runner that takes effort as a setting of its next turn
// rather than as a command turn (#2586): the Codex runner, where the text
// "/effort <level>" would start a model turn. deliverSettingsInBand hands such
// a runner the effort instead of sending the command. Claude's runner does not
// implement it, so the /effort turn stays the default: a runner that lacks the
// method gets the visible command, never a silently skipped change.
type effortSetter interface {
	SetEffort(effort string) error
}

// deliverSettingsInBand writes the settings changes implied by update onto id's
// live child stdin as the non-restarting live-apply (#1581): model as a set_model
// control request, effort as an ordinary /effort user turn (SetEffort on an
// effortSetter instead), and the resulting permission POSTURE
// as a set_permission_mode control request via SetPermissionMode (#1604 built the
// revoke-only form; #2043 generalised it). Caller must have released p.mu and must
// have installed the recomposed argv already, so a failed delivery still reaches
// the next spawn. merged is the posture that update RESULTS in, computed under
// p.mu by the caller, so this site reads no pool state.
//
// One send per PRESENT field, not per changed field: a frame carrying an
// unchanged model alongside a new effort re-sends the model, which claude answers
// and discards. That costs one round trip in a case no frame has been observed to
// produce, and per-field diffing is the refinement the presence contract rules
// out. The commands are SEPARATE turns — a two-command message is unmeasured —
// and model → effort → posture is fixed for the same reason claudeSettingsArgs
// fixes that order: determinism buys testability at no cost.
//
// The posture clause is EXACTLY ONE send for an update naming either posture
// field, and that arithmetic is the point (#2043's AC3). The posture is expressed
// by two fields, so a mode and a YOLO in one frame are one change, not two; the
// pre-#2043 shape — a mode clause beside the old !*update.YOLO → RevokeBypass()
// clause — would emit TWO identical control requests for one revocation, because
// the derivation makes a yolo:false update also carry a non-bypass mode. Dropping
// that clause without this replacement would emit ZERO and quietly end the
// revocation this path performs. The wire bytes of a revocation are unchanged by
// the collapse — RevokeBypass was SetPermissionMode("default") — so what changed
// is which method emits them, and that RevokeBypass is off the Runner seam.
//
// The value delivered is the posture that RESULTS, not the field the frame
// named: a yolo:false against a stored plan re-sends plan, which the child is
// already in. That is the same redundancy this path already tolerates for an
// unchanged model re-sent alongside a new effort, and it is reachable only when
// some other field changed too — an update that changes nothing returns as a
// no-op in UpdateSettings before the live-apply.
//
// THE BYPASS GUARD IS GONE (#2066), and its absence is load-bearing rather than a
// simplification. Until that ticket this site returned early for a merged posture of
// bypassPermissions, as "the second of three independent stops for the escalation,
// after the routing predicate and before the writer's own allow-list". All three
// stops existed because claude gated the escalation on the launch argv and refused
// the control request in words; #2065 removed that gate and #2060 measured the
// acceptance, so keeping any of them would mean the routing sends an escalation this
// site silently drops — an UpdateSettings that reports success and changes nothing.
// TestPool_DeliverSettingsInBand_EnableWritesTheEscalation is the inverse of the
// test that used to assert the guard, and it is the red for a tree that restores it.
//
// What still stops an escalation is upstream and unchanged: internal/relay's
// validPermissionMode refuses bypassPermissions as a mode string, so the wire keeps
// exactly one spelling of the escalation (the YOLO bit), and Pool.UpdateSettings
// gates every stored posture through permissionModeKnown. This site is a delivery,
// not a policy.
//
// Two ordering facts a reader will otherwise get wrong:
//
//   - Model and posture are control requests and do not pass the turncommit gate;
//     effort remains a queued turn. The fixed call order is model, effort, posture,
//     so a blocked effort send also delays the posture write from this call.
//   - It changes the child's permission mode, not work already dispatched. A tool
//     call in flight when the request arrives is not torn down — the old restart
//     killed the child and so ended it. `interrupt` remains the verb for ending a
//     running turn. The in-flight window itself — a revoke arriving while a tool
//     call is already dispatched — is not measured live; only the turn boundary is.
//
// THE MODEL SENT IS THE FAMILY ALIAS, NOT THE FRAME'S VALUE (#2447). A session
// follows the latest model of its family, so a pinned id is rewritten to its family
// on the way out. cmd/pyry resolves a pinned pick to its family before validating
// and storing it, so this call is the sink's own guarantee rather than the only
// one. It cannot disagree with the argv: claudeSettingsArgs applies the same
// modelfamily.Alias to the merged settings Pool.UpdateSettings installed before
// releasing p.mu. An empty model never arrives here: inBandDeliverable sends it to
// the restart branch, whose argv names the default model's family when that
// default is pinned (composeSpawnArgs).
//
// Fire-and-forget: every write error is logged and swallowed, which is the
// contract Restart has had on this path since #842. The settings are already
// persisted and the argv already installed, so a failed write loses nothing and
// the caller still sees success. The errors also cannot be classified here —
// internal/sessions must not import internal/streamsup, which would invert the
// Runner seam — and do not need to be: the reachable set (no live child, a
// dropped turn, a wrapped pipe failure) all warrants the same response. The
// dominant case is an evicted session or one between spawns, where nothing is
// degraded, which is why the record is Info and not Warn.
//
// context.Background() is correct here rather than a shortcut: WriteTurn consults
// ctx only for the turncommit gate, and its own doc names the nil-gate case as
// the direct single-turn send this is. Plumbing a ctx would change the
// relay.SettingsUpdater seam signature for no observable gain. conversationID is
// "" — accepted for Runner conformance and unused by the stream runner.
//
// NEVER logged, at any level: the model, effort or permission-mode value, the
// payload bytes, the conversation id. #833 keeps settings values out of the daemon
// log and this path gets no exemption just because the value now travels as
// command text. The posture record satisfies that rule structurally rather than by
// discipline: "permission_mode" is the constant field NAME, and the seam's error
// carries no mode either — Runner.SetPermissionMode refuses an unsupported mode
// with a bare sentinel that does not echo the rejected string, which is why the
// error may be logged verbatim. That second clause is the one that survived #2043
// taking the no-mode revoke shorthand off the seam.
func (p *Pool) deliverSettingsInBand(id SessionID, sup Runner, update SettingsUpdate, merged SessionSettings) {
	notDelivered := func(setting string, err error) {
		p.log.Info("sessions: in-band settings command not delivered",
			"event", "sessions.settings.delivery_err",
			"session", id, "setting", setting, "err", err)
	}
	send := func(setting, command string) {
		if err := sup.WriteUserTurn(context.Background(), "", []byte(command)); err != nil {
			notDelivered(setting, err)
		}
	}
	if update.Model != nil {
		if err := sup.SetModel(modelfamily.Alias(*update.Model)); err != nil {
			notDelivered("model", err)
		}
	}
	if update.Effort != nil {
		if es, ok := sup.(effortSetter); ok {
			if err := es.SetEffort(*update.Effort); err != nil {
				notDelivered("effort", err)
			}
		} else {
			send("effort", "/effort "+*update.Effort)
		}
	}
	if update.PermissionMode == nil && update.YOLO == nil {
		return
	}
	if err := sup.SetPermissionMode(merged.PermissionMode); err != nil {
		notDelivered("permission_mode", err)
	}
}

// DefaultSettings returns the bootstrap session's currently-persisted
// SessionSettings (model, effort, YOLO) plus whether a bootstrap session exists
// to read from. When none exists (the embedded evicted-bootstrap host, or a
// zero-value &Pool{} map-miss) it returns (SessionSettings{}, false) so a
// consumer falls back to the daemon defaults; there is no error path.
//
// It resolves p.bootstrap fresh on each call — mirroring Default — so it stays
// correct across a session-id rotation (RotateID flips p.bootstrap under the
// write lock). SessionSettings is a value type, so the return is a snapshot
// copy with no aliasing of the pool's live field.
//
// Concurrency: reads sess.settings under p.mu (RLock); does NOT take
// Session.lcMu — settings is a p.mu-guarded field (the writer UpdateSettings
// holds p.mu write; the other reader saveLocked holds p.mu), so there is no
// torn read. This accessor is unavoidable: settings is a private field read
// only under Pool.mu, so a consumer outside internal/sessions cannot reach it
// (SessionInfo/List does not carry it).
func (p *Pool) DefaultSettings() (SessionSettings, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	sess := p.sessions[p.bootstrap]
	if sess == nil {
		return SessionSettings{}, false
	}
	return sess.settings, true
}

// SettingsFor returns the named session's currently-persisted SessionSettings
// (model, effort, YOLO), or ErrSessionNotFound if the pool holds no session
// under that id. It is the read half matching UpdateSettings' per-session write:
// before it, an outside caller could change any session's settings but could
// read only the bootstrap's, via DefaultSettings.
//
// SessionSettings is a value type, so the return is a snapshot copy and not a
// lease on pool state — no caller can mutate a session through it, and the copy
// may be one concurrent UpdateSettings stale by the time it is read. That
// staleness is inherent to any lock-releasing accessor and is the contract
// DefaultSettings and mintSettings already carry.
//
// The empty id is deliberately NOT special-cased: it misses p.sessions like any
// other unknown id and gets ErrSessionNotFound. That puts this method on
// UpdateSettings' side of an in-package disagreement and declines Lookup's
// convention, where "" resolves to the bootstrap. Read and write must agree, or
// a caller passing "" reads the bootstrap's settings and writes nowhere. The
// bootstrap is registered under a real UUID, so "" is never a key.
//
// No id validation: unlike writeMCPSettings, the id here names no file and never
// leaves the map lookup, so a malformed id is a map miss — already the correct
// answer. The error is returned bare rather than wrapped with the caller's id
// (as UpdateSettings returns it, and unlike ambiguousError, which echoes only
// ids already resident in the pool), so a hostile or malformed id cannot be
// reflected into a log line or wire frame a consumer builds from the error.
//
// Read-modify-write warning for consumers: a caller that reads this triple and
// then calls UpdateSettings must send only the fields it intends to change.
// Echoing the whole snapshot back re-asserts a YOLO posture an operator may have
// cleared in the interval — SettingsUpdate's pointer-per-field presence contract
// makes sending one field the easy path, and this is the trap mintSettings
// avoids by rebuilding its literal field by field.
//
// Concurrency: MUST be called with p.mu unheld — one RLock acquisition per call,
// with no second lock and no delegation to DefaultSettings, whose own RLock
// would double-acquire. Go's RWMutex is not reentrant, so either shape
// self-deadlocks as soon as a writer queues between the two acquisitions; this
// is the hazard mintSettings' docstring already records. Reads sess.settings
// under p.mu (RLock) and deliberately NOT Session.lcMu — settings is a
// p.mu-guarded field (writer UpdateSettings holds p.mu write; the other reader
// saveLocked holds p.mu), so there is no torn read.
//
// DefaultSettings is not made redundant by this and must not be rewritten to
// call it: SettingsFor(p.BootstrapID()) is TWO acquisitions with a rotation
// window between them — RotateID can flip p.bootstrap under the write lock after
// the first returns — so the composed form can read a session that is no longer
// the bootstrap. DefaultSettings resolves p.bootstrap and reads settings under a
// single acquisition and stays the atomic way to ask for the bootstrap's
// settings.
func (p *Pool) SettingsFor(id SessionID) (SessionSettings, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	sess, ok := p.sessions[id]
	if !ok {
		return SessionSettings{}, ErrSessionNotFound
	}
	return sess.settings, nil
}

// DormantSettingsFor returns the SessionSettings a Revive of id would
// materialise — the model and effort id's own persisted entry carries, and the
// default posture — or ErrSessionNotFound if this pool holds no dormant entry
// under that id. It is SettingsFor's dormant half: the two partition the ids the
// daemon has a record of, because materialise retires the entry it takes over
// and Remove drops the one it deletes (see Pool.dormant).
//
// It exists so request_session_settings can answer a conversation bound to a
// session this daemon has not materialised (#2449). Pool.New materialises only
// the bootstrap, so after a restart that is EVERY conversation until its first
// message revives it, and the reply's every field sat at its zero: a client's
// model and effort menus went inert and its model label resolved the daemon's
// default row — a model the channel is not on.
//
// A SECOND READ rather than a fallback inside SettingsFor, deliberately. Folding
// it in would change what "not found" means for every other caller of the live
// read, several of which use exactly that answer to decide a session is not
// addressable. Here the two meanings are kept apart and the caller composes them.
//
// NOT Pool.revivedSettings, which is the same map read and cannot be reused as it
// stands: it collapses "no entry" into the zero SessionSettings, which is correct
// for a revive (an id with no record revives to claude's defaults) and wrong for
// a reader, which would then report an unknown bound id beside empty settings.
// The miss is this method's own.
//
// THE POSTURE IS BUILT, NOT CLEARED. The literal names Model and Effort only and
// is then canonicalised, so YOLO false and the default mode are structural:
// mintSettings' and revivedSettings' recorded reason, which is that a clearing
// statement is something a later edit can delete, and that a field added to
// registryEntry is not inherited until someone opts it in. Two things follow that
// a caller depends on. A restart stays a revocation point for a phone-granted
// permission bypass (#1487): a persisted yolo or permission_mode cannot reach
// this reply however the entry was written. And the reported posture is the one
// Revive will actually materialise, because canonicalSettings here is the same
// function buildSession applies to what Revive hands it — an agreement by
// construction rather than by two places spelling the same constant.
//
// Concurrency: MUST be called with p.mu unheld — one RLock acquisition, no
// delegation to another locking accessor. Go's RWMutex is not reentrant, so a
// call from inside a critical section self-deadlocks as soon as a writer queues;
// this is the hazard SettingsFor and mintSettings already record. The returned
// value is a snapshot copy, not a lease: SessionSettings is a value type and
// registryEntry is stored by value in p.dormant.
//
// No id validation and no logging, SettingsFor's posture verbatim: the id names
// no file and never leaves the map lookup, so a malformed id is a map miss —
// already the correct answer — and the error is returned bare rather than
// wrapped with it, so a hostile id cannot be reflected into a log line or a wire
// frame a consumer builds from the error.
func (p *Pool) DormantSettingsFor(id SessionID) (SessionSettings, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	entry, ok := p.dormant[id]
	if !ok {
		return SessionSettings{}, ErrSessionNotFound
	}
	return canonicalSettings(SessionSettings{
		Model:  entry.Model,
		Effort: entry.Effort,
	}), nil
}

// DormantStoredPostureFor reads the persisted posture for an agent switch.
// DormantSettingsFor intentionally reports Revive's revoked default posture;
// the switch instead carries a stored non-bypass mode to the new session.
func (p *Pool) DormantStoredPostureFor(id SessionID) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	entry, ok := p.dormant[id]
	if !ok {
		return "", ErrSessionNotFound
	}
	return settingsFromEntry(entry).PermissionMode, nil
}

// mintSettings returns the SessionSettings a freshly-minted session starts
// with: the operator's configured model and effort level, sourced from the
// bootstrap session's persisted settings so a new conversation does not fall
// back to claude's own defaults.
//
// Built field by field rather than by copying DefaultSettings' return, so YOLO
// is excluded structurally rather than by a clearing statement someone could
// later delete: a phone-granted escalation can never reach a minted session's
// POSTURE. (Since #2065 it cannot reach its argv either, because the flag is
// there unconditionally and carries no posture; what the excluded YOLO bit buys
// is that the minted session's stored mode stays non-escalated, so its child is
// written back down to that mode at every spawn.) Any field added in future is
// likewise not inherited until someone opts it in. That is the fail-closed
// direction and it is the same reasoning Revive's docstring records (#1487).
//
// It resolves p.bootstrap and reads its settings DIRECTLY rather than through
// DefaultSettings, which takes an RLock of its own. Since #2492 both settings
// sources are evaluated under the CALLER's p.mu — materialise reads this inside
// the critical section that retires a dormant entry — and Go's RWMutex is not
// reentrant, so a delegating body would self-deadlock as soon as a writer queued.
// The two reads stay in one critical section either way, which is
// DefaultSettings' own atomicity property against a RotateID landing between
// them; it is now the caller's to hold rather than this function's.
//
// The nil-bootstrap arm is that delegation's other half made explicit:
// DefaultSettings used to supply the zero SessionSettings for a pool with no
// bootstrap and this function discarded its existence bool. The no-configuration
// argv still falls out of the zero value.
//
// Concurrency: MUST be called with p.mu HELD, read or write. Takes no lock
// itself. This is the inverse of the contract it carried before #2492, and the
// inversion is shared with revivedSettings — see settingsSource. Its three
// callers hold the lock accordingly: materialise (write, via the source it is
// passed as), CreateIn (read, around its own call).
func (p *Pool) mintSettings() SessionSettings {
	boot := p.sessions[p.bootstrap]
	if boot == nil {
		return SessionSettings{}
	}
	return SessionSettings{
		Model:  boot.settings.Model,
		Effort: boot.settings.Effort,
	}
}

// revivedSettings returns the SessionSettings a revive of id starts from: the
// model and effort id's own dropped entry persisted, and no posture. An id with
// no dormant entry yields the zero value, which is what every revive got before
// #2448 — so an unknown id, and an entry that persisted neither field, both
// revive exactly as they did.
//
// The asymmetry is the whole decision, and it is narrower than it looks. A
// restart stays a revocation point for a permission bypass (#1487): a persisted
// yolo or permission_mode is not read here, so it cannot reach the revived
// session however the entry was written. Model and effort carry no privilege and
// are the operator's choice for that conversation, so dropping them only made the
// next turn run under claude's defaults.
//
// Built field by field rather than from settingsFromEntry with the posture
// cleared afterwards, matching mintSettings above for mintSettings' own recorded
// reason: the posture is then excluded STRUCTURALLY rather than by a clearing
// statement someone could later delete, and any field added to registryEntry in
// future is likewise not inherited until someone opts it in. That is the
// fail-closed direction. The cleared posture is spelled out downstream —
// buildSession's canonicalSettings turns the zero value into the default mode,
// exactly as it did for the zero value this replaces.
//
// Concurrency: MUST be called with p.mu HELD, read or write. Takes no lock
// itself, which is mintSettings' contract too — see settingsSource, the seam both
// are passed through.
//
// That contract is #2492's, and it inverts what this function carried before.
// Revive used to evaluate it as an ARGUMENT to materialise, so this RLock was
// released before materialise's write lock deleted the entry, and the previous
// revision of this paragraph called the gap benign — reasoning only about a
// concurrent REVIVE, which does land on materialise's take path and does drop the
// caller's settings by contract. What it did not reason about was a concurrent
// dormant WRITE, which had no writer until Pool.UpdateDormantSettings (#2463):
// such a write was persisted, acknowledged, and then deleted by the retirement,
// and the session came up on the value read before it. materialise now evaluates
// this INSIDE the critical section that retires the entry, so the read and the
// delete cannot be split and no window remains between them.
func (p *Pool) revivedSettings(id SessionID) SessionSettings {
	entry, ok := p.dormant[id]
	if !ok {
		return SessionSettings{}
	}
	return SessionSettings{
		Model:  entry.Model,
		Effort: entry.Effort,
	}
}

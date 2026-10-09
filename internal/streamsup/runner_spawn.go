package streamsup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/pyrycode/pyrycode/internal/canonicalpath"
	"github.com/pyrycode/pyrycode/internal/transcript"
)

// bypassPermissionsFlag is claude's argv spelling of the escalation, and the only
// spelling of it.
//
// NOTHING IN THIS PACKAGE'S PRODUCTION PATH READS IT ANY MORE, and that absence is
// the point rather than an oversight. Until #2065 its presence in a spawn's args was
// "the single deterministic yolo signal", and spawnAndWait consulted it to suppress
// the spawn-time posture write at a child launched in bypass. #2065 made
// sessions.claudeSettingsArgs append it to EVERY argv, so its presence stopped
// distinguishing anything and the decision moved to Config.OperatorBypass, which
// carries the flag's PROVENANCE across the seam. Reintroducing a reader here would
// reintroduce the universally-true predicate that ticket exists to remove.
//
// It is retained rather than deleted so the flag keeps exactly one spelling in this
// package for the spawn tests that compose an argv with it, instead of that literal
// migrating into a test file where the next reader would not find it.
//
// This is the FLAG, not the mode name; permissionModeBypass is the mode. #1603 kept
// this package's production source empty of the mode literal so the escalation
// stayed structurally absent from the write path, and #2066 ended that: the writer's
// allow-list admits the escalation now, so the mode has a named constant beside
// permissionModeDefault. The two are different strings for one posture, neither is
// derived from the other, and nothing here composes an argv from either.
const bypassPermissionsFlag = "--dangerously-skip-permissions"

// Restart swaps the live spawn base argv and, if a child is currently running,
// forces it to exit so the Run loop relaunches with the new args (resuming via
// --resume, since firstRun is already false after the first spawn). When no child
// is running it only swaps the args — they take effect on the next spawn.
// Non-blocking, fire-and-forget: the forever-retry Run loop guarantees the
// relaunch. Safe from any goroutine. Mirrors supervisor.Restart.
//
// It drives only Runner-internal state (restartMu, a ctx cancel, a buffered
// channel); it never touches Pool.mu or Session.lcMu, so the sessions layer can
// call it after releasing Pool.mu with no lock-order concern.
//
// The restartCh hint is always sent (non-blocking): a restart during a run makes
// the post-spawn drain skip backoff, and a restart during backoff (no live child
// to cancel) breaks the backoff wait. Coalescing is correct — two rapid restarts
// overwrite args with the newest value and collapse to the single buffered token,
// forcing one relaunch with the latest args.
//
// The argv install runs through setArgsLocked INSIDE this one section, not by
// calling SetSpawnArgs: the exported swap-only method takes restartMu itself, so
// calling it here would split Restart into two acquisitions and reopen the #1481
// window beginSpawn's doc closes. See SetSpawnArgs for the swap without the kill.
func (r *Runner) Restart(args []string) {
	r.restartMu.Lock()
	r.setArgsLocked(args)
	cancel := r.iterCancel
	r.restartMu.Unlock()

	// Hint first, then kill, so Run's post-spawn drain observes the token even if
	// the child exits the instant it is cancelled.
	select {
	case r.restartCh <- struct{}{}:
	default:
	}
	if cancel != nil {
		cancel()
	}
}

// SetSpawnArgs installs the base argv the NEXT spawn will use and leaves any live
// child running — it is Restart's swap half without the kill: no restartCh hint,
// no iterCancel call. It exists for a caller that needs to change what the session
// will run next without ending what it is running now; declining to call Restart
// is not that path, since it loses the swap outright and the next spawn (a
// crash-respawn, or an evict then Activate) silently re-execs the stale argv.
// Non-blocking, fire-and-forget, safe from any goroutine.
//
// The two do NOT converge when no child is live. Restart still sends its hint in
// that case, and the token is observable twice over: it satisfies Run's restartCh
// case in the backoff select, cutting an in-progress backoff wait short, and with
// no wait in flight it persists in the buffered channel so the NEXT child exit
// skips backoff entirely — RestartCount never increments and PhaseBackoff is never
// set. SetSpawnArgs sends nothing, so a runner already backing off stays backing
// off and the swap simply lands on the spawn that wait was going to make anyway.
//
// It takes restartMu exactly once and writes only args — never sessionID,
// rotatePending or iterCancel — so it preserves the #1481 single-acquisition
// property by construction, and cannot reach the forbidden state beginSpawn's doc
// names (that state is defined over the three fields it does not touch). Against a
// racing beginSpawn it serialises wholly before (that spawn observes the swap) or
// wholly after (that spawn keeps the old argv and the next one takes the new).
// Both are correct: the contract is the NEXT spawn, and it promises nothing about
// a spawn already in flight. Like Restart it drives only Runner-internal state and
// touches neither Pool.mu nor Session.lcMu, so the sessions layer can call it
// after releasing Pool.mu with no lock-order concern.
//
// The argv is installed VERBATIM — no validation, no shaping. That boundary is
// deliberate: composition and validation live upstream in the sessions layer
// (Session.spawnArgs, where claudeSettingsArgs enforces the yolo fail-safe in one
// place), and duplicating them here would give two places to keep in sync. Two
// consequences the caller owns, both inherited from Restart rather than introduced
// here: cmd/pyry's construction-time argv shaping is NOT reapplied on any
// post-construction install path (stripSessionIDFlags runs in mapStreamsupConfig
// and withApprovalArgs in newStreamRunnerFactory, both construction-only), and Run
// logs the composed argv at Info ("spawning claude"), so anything installed here
// reaches the daemon log.
//
// Must never be called with restartMu already held — the mutex is not reentrant.
// A caller already inside a section installs through setArgsLocked instead.
func (r *Runner) SetSpawnArgs(args []string) {
	r.restartMu.Lock()
	r.setArgsLocked(args)
	r.restartMu.Unlock()
}

// setArgsLocked installs the next spawn's base argv. Caller MUST hold restartMu.
// It is THE sole assignment to r.args after construction: Restart and SetSpawnArgs
// both install through it, which is what lets Restart keep its single restartMu
// acquisition (#1481) while there stays exactly one writer. Re-fusing the
// assignment back into a caller reintroduces the second writer this exists to
// prevent.
//
// The clone is load-bearing, not hygiene. Without it the caller keeps a live
// handle on the runner's spawn argv and can mutate it AFTER installation — a data
// race against beginSpawn's read, and a mutation that lands in the exec argv after
// whatever validation the caller performed. beginSpawn's "buildArgs is pure and
// copies base into a fresh slice, so passing r.args needs no clone" is about
// handing r.args OUT of the section; it does not license dropping the clone on the
// way IN.
//
// It makes no call-out. restartMu is a leaf: nothing under it may take another
// lock or do synchronous I/O, which is why Run logs "spawning claude" below
// beginSpawn's section rather than inside it. A diagnostic belongs in SetSpawnArgs
// after the unlock.
func (r *Runner) setArgsLocked(args []string) {
	r.args = slices.Clone(args)
}

// SetSpawnWorkDir installs the directory the NEXT spawn chdirs into, together
// with the transcript folder that spawn's create/resume probe reads. It does NOT
// restart: a live child keeps running where it is, and the swap lands on whatever
// spawn happens next — the crash-respawn the loop was going to make anyway, or
// the one a following RestartFresh orders. #1475's consumer pairs it with
// RestartFresh for exactly that reason.
//
// It is SetSpawnArgs' sibling. One restartMu acquisition, writing only the
// directory pair — never sessionID, rotatePending, freshSeq, iterCancel or args —
// so it preserves the #1481 single-acquisition property by construction and
// cannot reach the forbidden state beginSpawn's doc names (that state is defined
// over fields it does not touch). Against a racing beginSpawn it serialises
// wholly before (that spawn observes the swap) or wholly after (that spawn keeps
// the old directory and the next one takes the new); both are correct, because
// the contract is the NEXT spawn and it promises nothing about a spawn already in
// flight. Like Restart it drives only Runner-internal state and touches neither
// Pool.mu nor Session.lcMu, so the sessions layer can call it after releasing
// Pool.mu with no lock-order concern.
//
// THE RESOLVE SITS ABOVE THE LOCK, and both halves of that placement are
// load-bearing. Above, because restartMu is a leaf — nothing under it may take
// another lock or do synchronous I/O, the invariant setArgsLocked states — and
// canonicalpath.Resolve reads filesystem metadata and resolves symlinks.
// Present at all, because New applies the same function to cfg.WorkDir: skipping
// it would break workDir's "resolved absolute path" invariant and, worse,
// desynchronise the pair
// — the caller derives claudeSessionsDir using the same filesystem-path
// semantics, including on-disk casing, so a raw workDir here would name a
// directory whose transcript folder is the resolved one's. The supplied
// transcript directory is installed verbatim, never resolved here.
//
// A resolve failure writes NEITHER field: the pair is fail-closed on the
// directory the runner already had, never half-applied. That is the same
// direction the whole #1475 reject set takes — a workspace that cannot be
// resolved leaves the successor where it was rather than moving it somewhere
// unvalidated.
//
// An empty workDir is a no-op (logged at Warn): the runner never chdirs into "".
// This is the deterministic last-resort guard RestartFresh's empty-id early
// return already models — the validating boundary is the caller (cmd/pyry's
// resolveSpawnDir, which answers "" for "no recorded workspace" and never asks
// for an install), and this upholds New's non-empty contract regardless.
//
// Must never be called with restartMu already held — the mutex is not reentrant.
func (r *Runner) SetSpawnWorkDir(workDir, claudeSessionsDir string) error {
	if workDir == "" {
		r.log.Warn("streamsup: SetSpawnWorkDir called with empty dir; ignoring")
		return nil
	}
	resolved, err := canonicalpath.Resolve(workDir)
	if err != nil {
		return fmt.Errorf("streamsup: resolve spawn workdir: %w", err)
	}
	r.restartMu.Lock()
	r.workDir = resolved
	r.claudeSessionsDir = claudeSessionsDir
	r.restartMu.Unlock()
	return nil
}

// ClaudeSessionsDir reports the folder the live spawn inputs name — where claude
// writes this session's <id>.jsonl, and what the next spawn's create/resume probe
// will read. "" means no probe is configured.
//
// It exists so cmd/pyry's transcript reader can answer for the directory the
// session is CURRENTLY spawning in rather than the one it was constructed with:
// since #1475 a rotation can move that, and a stored copy would report the
// pre-move folder — the #2423 reading ("Context: 0%") re-introduced for exactly
// the conversations that moved.
//
// It takes restartMu alone, never mu, so it is safe to call from any goroutine
// and adds no edge to the daemon's lock order — the treatment liveSessionID
// already gets for the same reason.
func (r *Runner) ClaudeSessionsDir() string {
	r.restartMu.Lock()
	defer r.restartMu.Unlock()
	return r.claudeSessionsDir
}

// RestartFresh rotates the runner's persistent session id to newID and forces the
// next spawn to use --session-id <newID> (a fresh transcript, no fork),
// re-establishing first-run semantics. A subsequent crash-respawn then --resumes
// newID — never the pre-rotation id. If a child is live it is cancelled so Run
// relaunches immediately (skipping backoff, exactly like Restart); with no live
// child the rotation takes effect on the next spawn. Non-blocking,
// fire-and-forget, safe from any goroutine.
//
// An empty newID is a no-op (logged at Warn): the runner never emits
// --session-id "". This is a deterministic last-resort guard upholding New's
// non-empty contract — the validating boundary is the pool/routing layer (#1125),
// per the "caller-supplied id validation at the primitive boundary" convention.
// The early return sits above the section below, so a no-op does NOT bump freshSeq:
// the rotation gate's release authorisation counts rotations that landed, and no
// production path pairs an arm with an empty-id call (startFreshRunner passes only
// rotate()'s success value, and RotateForNewSession returns ("", err) on every
// failure). If a pool bug ever minted an empty id the arm would outlive it until
// the next rotation — the conservative side, since the alternative authorises a
// spawn that succeeded no rotation at all.
//
// Unlike Restart it leaves r.args untouched: new_session rotates the id, not the
// model/flags — args stay owned by Restart/UpdateSettings. Like Restart it drives
// only Runner-internal state (restartMu, a ctx cancel, restartCh) and never a
// Pool lock, so the sessions layer can call it after releasing Pool.mu.
func (r *Runner) RestartFresh(newID string) {
	if newID == "" {
		r.log.Warn("streamsup: RestartFresh called with empty id; ignoring")
		return
	}
	r.restartMu.Lock()
	r.sessionID = newID
	r.rotatePending = true
	// Bumped in the SAME section as the id rotation, so beginSpawn's snapshot can
	// never skew against it: a spawn's section lands wholly before this one (its
	// snapshot is not the successor's) or wholly after (it is).
	r.freshSeq++
	cancel := r.iterCancel
	r.restartMu.Unlock()

	// Announce the rotation BELOW the unlock and ABOVE the teardown, and both
	// halves of that placement are load-bearing. Below, because restartMu is a leaf
	// — nothing under it may take another lock or do synchronous I/O — and this is
	// an arbitrary consumer-supplied function, so firing it inside the section would
	// make the leaf property depend on what the consumer installed. Above, because
	// the cancel below starts a teardown the Run loop answers with a fresh spawn on
	// its own goroutine: a callback fired after it races that spawn, and the loser
	// is the successor child's first events, tagged with the id this rotation just
	// replaced. See Config.OnSessionRotate for the window that ordering accepts in
	// exchange.
	if r.cfg.OnSessionRotate != nil {
		r.cfg.OnSessionRotate(newID)
	}

	// Hint first, then kill, so Run's post-spawn drain observes the token even if
	// the child exits the instant it is cancelled (identical ordering to Restart).
	select {
	case r.restartCh <- struct{}{}:
	default:
	}
	if cancel != nil {
		cancel()
	}
}

// AdoptSessionID installs newID as the live session id the runner's NEXT spawn
// resumes, and does nothing else (#2136). It exists for claude's OWN announced
// conversation reset, which the daemon follows rather than orders: the pool
// registry, the sink tag and the drain's gate all move onto the announced id,
// and without this the runner keeps the pre-reset one and its next crash-respawn
// --resumes the conversation the operator just cleared.
//
// It is SetSpawnArgs' mirror image. That method takes restartMu exactly once and
// writes one field, and its doc names the three it is careful not to touch; this
// one takes restartMu exactly once and writes the first of those three,
// sessionID, touching neither rotatePending nor freshSeq nor iterCancel, and
// sending no restartCh hint. So beginSpawn's #1481 single-acquisition property
// survives by construction here for the same reason it does there, and the state
// beginSpawn's doc forbids — defined over the fields this does not write — stays
// unreachable. Against a racing beginSpawn it serialises wholly before (that
// spawn resumes the announced id) or wholly after (that spawn keeps the previous
// id and the next one takes the announced one); both are correct, because the
// contract is the NEXT spawn and it promises nothing about one already in flight.
//
// What it deliberately is NOT is RestartFresh with the teardown removed. That
// method rotates to an id the DAEMON minted and re-establishes first-run
// semantics, so its trio arms a fresh transcript and its cancel makes the
// successor child bind to it. Here claude has ALREADY reset in-process: the
// transcript exists, the child running now is the one writing to it, and there is
// nothing to re-establish and nothing to relaunch. Arming rotatePending would make
// the next spawn emit --session-id against a live transcript, which claude refuses
// (ADR 032); bumping freshSeq would authorise a spawn through the rotation gate
// that succeeded no rotation.
//
// An empty newID is refused at Warn and nothing is written — the same last-resort
// guard on New's non-empty contract RestartFresh carries, and above the section
// for the same reason. The provenance argument is NOT RestartFresh's, though the
// conclusion matches: that method's callers hand it a daemon-minted id, whereas
// this one's caller hands it a value claude wrote. What makes the guard sufficient
// rather than a validator is the boundary upstream — the parser's
// emitConversationReset gates on transcript.ValidStem, an anchored full match over
// a canonical lowercase UUID stem, so the id can carry no separator, no traversal,
// no leading dash and no unbounded length by the time it reaches here. Nothing in
// this package re-derives that shape, and nothing here joins a path with it: the
// spawn's --session-id / --resume choice stays useCreateForm's, whose StatByID
// probe runs ValidStem itself before any join.
//
// Like Restart and SetSpawnArgs it drives only Runner-internal state and takes no
// Pool lock, so the sessions layer can call it after releasing Pool.mu with no
// lock-order concern. restartMu stays a leaf: no call-out, no second lock, no I/O
// under the section. Non-blocking and safe from any goroutine.
func (r *Runner) AdoptSessionID(newID string) {
	if newID == "" {
		r.log.Warn("streamsup: AdoptSessionID called with empty id; ignoring")
		return
	}
	r.restartMu.Lock()
	r.sessionID = newID
	r.restartMu.Unlock()
}

// beginSpawn captures one iteration's spawn inputs — the live (possibly
// Restart-swapped) base argv and the (possibly RestartFresh-rotated) id pair,
// consuming the fresh-restart request — AND publishes that iteration's cancel, in
// a SINGLE restartMu section. That single acquisition is the whole point (#1481):
// a racing Restart/RestartFresh takes restartMu exactly once, so it is serialised
// either fully before this section (the spawn observes its swap) or fully after it
// (it finds the just-published cancel and tears this spawn down). There is no
// third position, so the forbidden outcome — a live child under a pre-rotation id
// with no live iteration cancel — is unreachable rather than merely narrowed.
//
// SetSpawnArgs is the third racer on restartMu (#1580) and the weakest: one
// acquisition like the other two, and it writes args only — never sessionID,
// rotatePending or iterCancel — so whichever side of this section it lands on, the
// worst it can do is leave the swap for the following spawn.
//
// It reads the fields directly instead of calling accessors: restartMu is not
// reentrant, so any helper that takes it (as the now-deleted liveArgs and
// nextSpawnID accessors did) would deadlock here. buildArgs is pure and copies
// base into a fresh slice, so passing r.args needs no clone — and handing that
// slice header OUT of the section is safe for the same reason setArgsLocked
// clones on the way IN: that clone makes setArgsLocked the sole writer of a
// backing array no installer ever mutates after publication, so a later install
// swaps the header and leaves this spawn's local pointing at the old, immutable
// array.
//
// The transcript probe and the argv assembly deliberately sit BELOW the unlock
// (#1630). restartMu is a leaf — nothing under it may take another lock or do
// synchronous I/O, the invariant setArgsLocked states — and useCreateForm's
// os.Stat is exactly that class of call-out; the mutex it would stall on a hung
// $HOME is the one liveSessionID takes on WriteUserTurn's diagnostic path.
// Moving them out does not re-split #1481's section: the forbidden outcome is a
// live child under a pre-rotation id with NO live iteration cancel, and the
// section still reads the spawn inputs and publishes iterCancel together, so a
// racer arriving after the unlock finds the published cancel and tears this
// spawn down. Only pure assembly moved, into a window that already exists (Run's
// "spawning claude" log and spawnAndWait's setup both run in it). The id the
// probe targets is the one snapshotted in the same section that consumed
// rotatePending, so the decision can never skew against a racing rotation.
//
// forceFirst reports that a fresh-restart request was consumed; the caller re-arms
// its Run-goroutine-private firstRun on true. Returning it rather than a new
// firstRun keeps that local a plain assignment at the call site, where a := would
// shadow it and silently break the started-gated flip.
//
// freshSeq is this spawn's snapshot of the landed-rotation counter, threaded
// through spawnAndWait to setStdin, where it authorises (or refuses) the rotation
// gate's release (#1482). It is a READ inside the section that already exists, not
// a second acquisition, so the one-acquisition-per-spawn-setup charter holds. The
// snapshot must be taken HERE, at spawn SETUP, and never re-read at bind time:
// the crash-respawn this closes runs its beginSpawn before RestartFresh but can
// reach setStdin after it, so a bind-time read would see the bumped value, disarm,
// and be killed moments later by the very rotation that bumped it. "Set up before
// that rotation's RestartFresh landed" is a statement about setup time, and only a
// setup-time snapshot carries it.
// spawnMode is this spawn's snapshot of the posture to assert, threaded through to
// spawnAndWait. It is a READ inside the section that already exists, not a second
// acquisition, so the one-acquisition-per-spawn-setup charter holds — the same
// treatment freshSeq gets above.
//
// env is this spawn's child environment, composed below the unlock from the SAME
// snapshotted id the argv is built from (#2169). Composing it here rather than
// letting spawnAndWait read a live id is what makes "the environment and the argv
// name one session" true by construction: a second restartMu acquisition at spawn
// time could land on the far side of a racing rotation and skew the two apart, and
// would also break the one-acquisition charter above.
//
// workDir is this spawn's snapshot of the directory to chdir into, threaded
// through to spawnAndWait's cmd.Dir and to Run's "spawning claude" log — both of
// which read the live field until #1475 made it swappable, which is precisely
// what a snapshot removes: a live read races SetSpawnWorkDir. The transcript
// folder is snapshotted in the SAME section and consumed locally by useCreateForm
// below, which is what makes "the directory and the transcript probe name one
// place" true by construction, the same way env and args name one session.
//
// The return list deliberately places no two same-typed results adjacent: workDir
// sits between env []string and forceFirst bool, spawnMode after freshSeq
// uint64, and id — the session id the argv was built from, returned for Run's
// exit record (#2723) — between cancel and args, so transposing two adjacent
// results at the call site is a compile error rather than a child spawned in a directory named "acceptEdits". That is
// boundRunSettings' stated reasoning for named-field wiring, applied to a return
// list that cannot have names.
func (r *Runner) beginSpawn(ctx context.Context, firstRun bool) (
	iterCtx context.Context, cancel context.CancelFunc, id string, args, env []string,
	workDir string, forceFirst bool, freshSeq uint64, spawnMode string,
) {
	// Derived BEFORE the acquisition on purpose: context.WithCancel takes the
	// parent cancelCtx's own internal mutex, and restartMu must never be held
	// across another package's locking. It touches no Runner state, so nothing it
	// does can observe the pre-publish window.
	iterCtx, cancel = context.WithCancel(ctx)

	r.restartMu.Lock()
	forceFirst = r.rotatePending
	r.rotatePending = false
	base := r.args
	id = r.sessionID
	freshSeq = r.freshSeq
	spawnMode = r.spawnMode
	workDir = r.workDir
	sessionsDir := r.claudeSessionsDir
	r.iterCancel = cancel
	r.restartMu.Unlock()

	env = spawnEnv(r.cfg.Env, r.cfg.SessionIDEnvVar, id)
	args = buildArgs(base, useCreateForm(sessionsDir, id, firstRun || forceFirst), id,
		!promptSuggestionsDisabled(append(os.Environ(), env...)))
	return iterCtx, cancel, id, args, env, workDir, forceFirst, freshSeq, spawnMode
}

// useCreateForm reports whether this spawn's id flag should be --session-id
// (create) rather than --resume (reattach). Pure apart from a single os.Stat.
//
// With sessionsDir empty the probe is not consulted at all — no syscall is made
// — and latchCreate decides verbatim. That is what keeps a Runner constructed
// without the directory byte-identical to pre-#1630 argv, which is every
// production path until #1631 threads the directory through. With a directory
// supplied the probe decides OUTRIGHT and latchCreate is ignored, including on
// the FIRST spawn and including when a RestartFresh rotation re-armed first-run
// form: a confirmed by-id hit is the only answer that yields --resume, and every
// other answer — absent file, unreadable directory, an id ValidStem rejects —
// yields the create form. So the probe has no failure mode that can fail a
// spawn.
//
// The single rule is ADR 032's, carried into this package, and it closes two
// separate defects at once. A session that launched but ran no turn establishes
// no transcript (#1655), so the latch's true→false flip would have every respawn
// emit --resume against an id claude has no record of, which exits 1 (#1656) on
// a widening backoff forever; the probe reads absent and creates instead, and
// converges because a rejected --resume leaves NO STUB behind for the next probe
// to latch onto. And on a daemon restart the transcript survives, so the latch's
// first spawn emits --session-id against a live transcript, which claude refuses
// (ADR 032); the probe reads present and resumes. It also settles the
// rotated-id-collides-with-an-existing-transcript case as "resume" simply by
// falling out of the rule — unreachable from sessions.NewID's minting, never
// observed, and deliberately given no branch, flag or test of its own.
//
// Two things this must not become, both one edit away and both load-bearing.
// It probes via transcript.StatByID and never a hand-rolled
// filepath.Join(sessionsDir, id+ext) + os.Stat: StatByID runs its ValidStem gate
// BEFORE the join, and New checks only that SessionID is non-empty while
// RestartFresh re-checks nothing, so a non-canonical id reaches here and a
// hand-rolled join would turn it into an arbitrary-path existence oracle that
// flips the spawn's id flag. And every non-hit falls back to the create form,
// NEVER to a directory scan: transcript.Newest sits beside StatByID and answers
// a superficially similar question, but #839 deleted --continue and the
// adopt-by-mtime scan precisely to close the confused-deputy gap where a restart
// adopts a DIFFERENT claude's newer transcript out of the shared sessions dir.
func useCreateForm(sessionsDir, id string, latchCreate bool) bool {
	if sessionsDir == "" {
		return latchCreate
	}
	// The error is discarded because absence is the expected answer here, not a
	// failure: StatByID reports every miss as the zero Result, so !Found() already
	// covers the absent file, the unreadable directory and the invalid stem. A
	// separate err != nil arm would be a return site no fixture can reach on its
	// own.
	res, _ := transcript.StatByID(sessionsDir, id)
	return !res.Found()
}

// buildArgs assembles one spawn's argv: the fixed stream-json prefix, then the
// caller's base args, then the id flag. create picks that flag's form:
// --session-id <sessionID> establishes the on-disk transcript under a known id,
// --resume <sessionID> reattaches to one that already exists (append, no fork).
// The caller decides which, and in production that decision is useCreateForm's —
// a by-id transcript existence probe when Config.ClaudeSessionsDir is set, the
// Run loop's firstRun latch when it is not.
// Passing the SAME sessionID to both is why the on-disk id is stable across a
// kill-and-restart — plain --resume reuses the id and does not fork
// (--fork-session is the explicit, unused opt-in). Never emits -p/--print: the
// non-print choice is billing-tied and spike-verified (multi-turn, interrupt,
// resume, and the approval round-trip all work without it). Pure — no Runner
// state, and it never mutates base.
func buildArgs(base []string, create bool, sessionID string, promptSuggestions bool) []string {
	args := make([]string, 0, len(base)+11)
	args = append(args,
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--forward-subagent-text",
		// claude echoes each user message where it reads it (#2730); the parser
		// turns the echo into a digest-only turnevent.UserEcho.
		"--replay-user-messages",
	)
	if promptSuggestions {
		// A stream-json child generates its native post-result suggestion only
		// when asked (#2831); the parser turns it into turnevent.PromptSuggestion.
		args = append(args, "--prompt-suggestions")
	}
	args = append(args, base...)
	if create {
		return append(args, "--session-id", sessionID)
	}
	return append(args, "--resume", sessionID)
}

// promptSuggestionEnv is claude's own switch for native prompt suggestions.
const promptSuggestionEnv = "CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION"

// promptSuggestionsDisabled reports whether the child environment env turns
// claude's prompt suggestions off (#2831). The last entry wins, as it does for
// the spawned process, and only the literal "false" disables. A settings-file
// promptSuggestionEnabled: false is claude's own to honour.
func promptSuggestionsDisabled(env []string) bool {
	disabled := false
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, promptSuggestionEnv+"="); ok {
			disabled = value == "false"
		}
	}
	return disabled
}

// mcpStatusEligible reports whether args confines this child to the daemon's one
// MCP config. Requiring exactly one matching config fails closed when duplicate
// flags could add an operator-controlled inventory.
func mcpStatusEligible(args []string, daemonConfigPath string) bool {
	if daemonConfigPath == "" {
		return false
	}
	strict := false
	configs := 0
	matchingConfigs := 0
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--strict-mcp-config":
			strict = true
		case args[i] == "--mcp-config":
			configs++
			if i+1 < len(args) {
				i++
				if args[i] == daemonConfigPath {
					matchingConfigs++
				}
			}
		case strings.HasPrefix(args[i], "--mcp-config="):
			configs++
			if strings.TrimPrefix(args[i], "--mcp-config=") == daemonConfigPath {
				matchingConfigs++
			}
		}
	}
	return strict && configs == 1 && matchingConfigs == 1
}

// spawnEnv composes one spawn's additions to os.Environ(): the caller's fixed
// Config.Env, then Config.SessionIDEnvVar bound to the id THIS spawn is using —
// beginSpawn's snapshot, the same value buildArgs just put in the argv. An empty
// name binds nothing and returns base as it stands, so a runner that does not opt
// in keeps inheriting the parent environment implicitly. Pure — no Runner state.
//
// It never appends INTO base: Config.Env belongs to the caller and every spawn of
// this runner reads it, so an in-place append into spare capacity would be a write
// one spawn could observe in another. The session variable goes LAST, so it wins
// over a same-named entry in Config.Env under os/exec's documented last-duplicate-
// wins rule — the fail-safe direction, since the runner's id is the live one.
func spawnEnv(base []string, name, sessionID string) []string {
	if name == "" {
		return base
	}
	out := make([]string, 0, len(base)+1)
	out = append(out, base...)
	return append(out, name+"="+sessionID)
}

// accountTokenEnv admits one attempt outside runner locks. The short-lived read
// context is separate from the iteration context used by the admitted child.
func (r *Runner) accountTokenEnv(ctx context.Context, env []string) ([]string, error) {
	return r.accountTokenEnvWithTimeout(ctx, env, 10*time.Second)
}

func (r *Runner) accountTokenEnvWithTimeout(ctx context.Context, env []string, timeout time.Duration) ([]string, error) {
	if r.cfg.AccountTokenProvider == nil {
		return env, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	token, failure, privateErr := r.cfg.AccountTokenProvider(readCtx)
	// Cancellation takes precedence even if a reader returns a token as success.
	if err := readCtx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, accountTokenError(AccountTokenTimeout)
		}
		return nil, accountTokenError(AccountTokenCancellation)
	}
	if deadline, ok := readCtx.Deadline(); ok && !time.Now().Before(deadline) {
		return nil, accountTokenError(AccountTokenTimeout)
	}
	if privateErr != nil || failure != "" {
		if failure == "" {
			failure = AccountTokenReadFailure
		}
		return nil, accountTokenError(failure)
	}
	if strings.HasSuffix(token, "\r\n") {
		token = strings.TrimSuffix(token, "\r\n")
	} else {
		token = strings.TrimSuffix(token, "\n")
	}
	if token == "" {
		return nil, accountTokenError(AccountTokenEmptyOutput)
	}
	if strings.ContainsRune(token, 0) || strings.ContainsFunc(token, unicode.IsSpace) {
		return nil, accountTokenError(AccountTokenInvalidOutput)
	}
	const prefix = "CLAUDE_CODE_OAUTH_TOKEN="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+token), nil
}

func accountTokenError(failure AccountTokenFailure) error {
	switch failure {
	case AccountTokenReadFailure, AccountTokenEmptyOutput, AccountTokenInvalidOutput,
		AccountTokenTimeout, AccountTokenCancellation:
		return errors.New("streamsup: account token: " + string(failure))
	default:
		return errors.New("streamsup: account token: rejected")
	}
}

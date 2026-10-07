package streamsup

import (
	"context"
	"io"
	"log/slog"
	"time"
)

// AccountTokenFailure is a safe rejection category. Unknown values are replaced
// with a generic reason; private provider errors and output are never exposed.
type AccountTokenFailure string

const (
	AccountTokenReadFailure   AccountTokenFailure = "read failure"
	AccountTokenEmptyOutput   AccountTokenFailure = "empty output"
	AccountTokenInvalidOutput AccountTokenFailure = "invalid output"
	AccountTokenTimeout       AccountTokenFailure = "timeout"
	AccountTokenCancellation  AccountTokenFailure = "cancellation"
)

// AccountTokenProvider reads a fresh account token for one launch attempt.
// Readers must honour ctx cancellation and must not log tokens or private errors.
// A non-nil error or nonempty category rejects even when token text is returned.
// Errors and token text are private; only allowlisted categories reach diagnostics.
// An empty category accompanying an error means AccountTokenReadFailure.
type AccountTokenProvider func(ctx context.Context) (token string, failure AccountTokenFailure, privateErr error)

// Config configures a Runner. Required fields are validated in New; zero values
// for optional fields fall through to the documented defaults.
type Config struct {
	// ClaudeBin is the resolved path to the claude executable. Required;
	// New surfaces a not-found error via exec.LookPath.
	ClaudeBin string

	// WorkDir is the child process's working directory. Required. New resolves
	// it via canonicalpath.Resolve (macOS /tmp → /private/tmp, the #989
	// symlink hazard); the resolved path is what cmd.Dir is set to and is the
	// only path canonicalisation this package performs.
	WorkDir string

	// SessionID is the caller-minted claude session id. Required (non-empty
	// check only — the pool owns minting and shape validation). With no
	// ClaudeSessionsDir the first spawn passes --session-id <SessionID> and every
	// respawn passes --resume <SessionID> (reattach, append, no fork); with one
	// set, the by-id transcript probe picks the form per spawn instead (see
	// useCreateForm). Either way the SAME id is passed, so the on-disk id stays
	// stable across a kill-and-restart.
	SessionID string

	// ClaudeSessionsDir is the directory containing claude's <uuid>.jsonl files
	// for this WorkDir. Empty disables the by-id transcript probe, and every
	// spawn's id flag falls back to the Run loop's firstRun latch — byte-identical
	// to pre-#1630 argv. Name and semantics mirror sessions.Config.ClaudeSessionsDir
	// deliberately, so the two read as one concept.
	//
	// It is NEVER derived from WorkDir inside this package.
	// sessions.DefaultClaudeSessionsDir maps a workdir into the real
	// $HOME/.claude/projects/<encoded>, so deriving it here would point every unit
	// test — whose WorkDir is a t.TempDir() — at the developer's actual home. The
	// directory only ever arrives through this field.
	ClaudeSessionsDir string

	// Args is the caller-supplied pass-through argv (e.g. --model <m>). streamsup
	// owns only the fixed stream-json prefix and the id flag; everything else is
	// the caller's, mirroring supervisor.buildClaudeArgs. New clones it.
	Args []string

	// Stdout receives the child's stdout. Optional; nil discards it (a plain
	// exec.Cmd with Stdout == nil sends the child's stdout to /dev/null, so an
	// unconsumed stream never blocks the child). #1088's turnevent parser is the
	// sink that plugs in here.
	Stdout io.Writer

	// ControlParser is the Parser in Stdout's writer path. It binds private
	// control-response correlation when Stdout wraps the parser, for example in a
	// live probe that also observes raw child output. Nil preserves the direct
	// *Parser inference used by the daemon. A non-nil value must be the exact parser
	// Stdout forwards every child byte to.
	ControlParser *Parser

	// Stderr receives the child's stderr. Optional; nil discards it.
	Stderr io.Writer

	// Env is appended to os.Environ() in the child process. Optional; tests use it
	// to thread the fake-child wiring, and production leaves it nil — a value that
	// varies per SPAWN cannot live on a construction-time field, which is why the
	// session identity #2169 puts on the child rides SessionIDEnvVar instead.
	//
	// A non-empty child environment makes the spawn set cmd.Env explicitly instead
	// of inheriting implicitly — the same set of variables either way, since an
	// exec.Cmd with a nil Env already inherits the parent's environment.
	Env []string

	// AccountTokenProvider optionally gates every launch, including retries and
	// restarts, on a fresh token read with a ten-second read-only deadline. Nil
	// preserves inherited credentials. Success replaces inherited and Env token
	// entries without changing either source. Rejection never launches a child.
	AccountTokenProvider AccountTokenProvider

	// SessionIDEnvVar names an environment variable each spawn binds to THAT
	// spawn's live session id — the same id its argv carries as --session-id /
	// --resume, composed in beginSpawn from the one snapshot buildArgs reads, so the
	// environment and the argv can never name two different sessions. Empty (the
	// default) binds nothing and leaves the child environment to Env alone.
	//
	// The NAME crosses this seam and not the value, and that is the whole point
	// (#2169). The interactive daemon needs the LIVE id here: cfg.SessionID is the
	// construction-time seed, RestartFresh rotates r.sessionID and respawns, so a
	// value composed by the caller at construction would name the retired session
	// for the entire life of every successor child. The consumer is the pyry_files
	// MCP server claude FORKS, which inherits this environment and forwards the id
	// as the destination of a handed-over file; the daemon refuses an id it has
	// retired rather than misfiling, so a stale value fails closed and stays broken.
	//
	// A /clear rotation is deliberately NOT tracked: it re-keys the pool without
	// respawning, so the running child keeps the environment it was exec'd with
	// until its next spawn. That boundary is stated in #2169's plan under
	// ## Revisions; closing it means reaching a live process's environment, which
	// no operating system here permits.
	SessionIDEnvVar string

	// Logger is used for lifecycle diagnostics. Optional; nil falls back to
	// slog.Default().
	Logger *slog.Logger

	// BackoffInitial, BackoffMax, BackoffReset configure the restart ladder.
	// Zero falls back to the supervisor defaults (500ms / 30s / 60s).
	BackoffInitial time.Duration
	BackoffMax     time.Duration
	BackoffReset   time.Duration

	// OnChildExit is called once per COMPLETED SUPERVISION ITERATION: after the
	// spawn attempt finishes and before Run decides what to do next (shut down,
	// relaunch immediately, or back off). Optional — nil-checked at the fire site
	// and left nil by tests that omit it — but NON-NIL on every production
	// construction path since #1210: the sole tree-wide caller of New installs a
	// per-runner turn-busy clear here, so a conversation whose child dies mid-turn
	// stops being reported busy. Exported, unlike onSpawn, because that consumer
	// lives outside this package.
	//
	// The cardinality is per iteration, NOT per live child: it also fires when
	// the spawn failed during setup and no claude process ever launched
	// (spawnAndWait's started == false). Deliberate — no child existed, so no
	// turn of this runner's can be open, and the intended consumer's clear is
	// idempotent. A caller that needs "a real child died" cannot get it here.
	//
	// It fires on every exit path: a crash (which falls through to the backoff
	// ladder), a deliberate restart (which relaunches immediately), and a
	// shutdown. The shutdown case works because the call sits ABOVE Run's
	// post-spawn ctx.Err() return, which itself sits above the "claude exited"
	// log — a parent cancel fires the callback before Run returns.
	//
	// It runs synchronously on the Run goroutine with NO Runner lock held, so it
	// may call any Runner method (Restart, RestartFresh, Interrupt, Stdin,
	// State), but it must NOT block: the restart ladder is stalled until it
	// returns, delaying the respawn. Slow work belongs on a channel or a
	// goroutine. It must not panic either — there is no recover here (matching
	// onSpawn), so a panic takes the supervise loop down with it.
	//
	// It fires strictly AFTER the child has exited: spawnAndWait returns only
	// past cmd.Wait or a pre-launch error, never while claude is still running,
	// and Stdin() already reports no live child by then. It is NOT a drain
	// barrier, though — it carries no data out of the child, and bytes the dead
	// child already wrote may still be in flight in a downstream sink when it
	// fires. A consumer needing ordering against those events must get it from
	// that sink, not from this callback.
	OnChildExit func()

	// OnCrashLoop is called once per CRASH EPISODE: when crashLoopFastExits
	// consecutive supervision iterations have entered the backoff ladder each with
	// an uptime under crashLoopFastUptime (#2724). Optional and nil-checked. The
	// interactive daemon turns it into a non-terminal session_error so clients hear
	// within seconds that claude cannot stay up, instead of after msgqueue's
	// two-minute give-up.
	//
	// It is fed from the backoff branch and nowhere else, which is the whole of
	// what separates it from OnChildExit. A deliberate restart (Restart,
	// RestartFresh) skips that branch, so it neither counts nor ends an episode; a
	// shutdown returns above it. A spawn that failed before claude launched does
	// reach it, with near-zero uptime, and counts. An exit that is not fast ends the
	// episode, and a later run of fast exits fires again.
	//
	// It carries nothing out of the child — no exit status, no stderr, no argv —
	// so a consumer cannot put child output on the wire through it.
	//
	// Same calling contract as OnChildExit: synchronous on the Run goroutine with
	// no Runner lock held, after the backoff state update and before the wait. It
	// must not block, since the respawn waits on it, and must not panic.
	OnCrashLoop func()

	// OnSessionRotate is called when RestartFresh ACCEPTS a rotation, with the id
	// it rotated onto. Optional — nil-checked at the fire site and left nil by
	// every construction path but one — and non-nil only on the interactive
	// daemon's, where newStreamRunnerFactory uses it to move the turn-event sink's
	// session tag off the id the runner was constructed with (#1133). Without it
	// that tag stays frozen while the pool rebinds the conversation to the new id,
	// and the drain's active-session gate drops every later event for that
	// conversation until the daemon restarts.
	//
	// Exported for OnChildExit's reason: the consumer lives outside this package.
	//
	// CARDINALITY IS PER ACCEPTED ROTATION, not per RestartFresh call and not per
	// spawn. RestartFresh's empty-id refusal returns ABOVE the fire site, so a
	// consumer's tag can never be emptied by a call the runner itself declined —
	// which matters because an empty tag matches no bound session and would black-
	// hole that conversation's stream for the life of the runner. A respawn that
	// merely re-uses the current id (Restart, or the backoff ladder) fires nothing:
	// this is the ROTATION signal, not a spawn signal.
	//
	// It fires strictly BEFORE the outgoing child is torn down and therefore before
	// the successor is spawned — above both the restart hint and the iteration
	// cancel. That ordering is the point rather than an implementation detail: fired
	// after the cancel, the Run loop could tear down and respawn concurrently, and
	// the successor's first events would carry the id the rotation just replaced,
	// which is the defect this seam exists to remove. The cost is the mirror window
	// — the outgoing child's residual stdout, parsed after this returns, is seen by
	// the consumer under the NEW id. For the sole consumer that is the same runner,
	// conversation and client, so it is a fidelity bound rather than a disclosure
	// one; see the plan's security review for the derivation.
	//
	// It runs synchronously on the CALLER's goroutine (whichever one called
	// RestartFresh — the daemon's new_session dispatch) with NO Runner lock held, so
	// it may call any Runner method. It must NOT block: the teardown of the outgoing
	// child is stalled until it returns. It must not panic either — there is no
	// recover here, matching OnChildExit and onSpawn.
	OnSessionRotate func(newID string)

	// RequestInitializeOnSpawn asks each spawned child, exactly once, to report
	// what the session knows about itself — the model list and the slash-command
	// list — by writing one initialize control_request onto its held-open stdin
	// (see RequestInitialize). Optional; false (the zero value) keeps a runner
	// byte-identical to pre-#1839 behaviour, which is what leaves every
	// construction site other than the interactive daemon's mapStreamsupConfig
	// untouched. Set it only there: the ask is that daemon's policy, not every
	// runner's behaviour.
	//
	// CARDINALITY IS PER SPAWN, and that is the whole of how "exactly once per
	// child" is enforced — there is no counter and no per-child bookkeeping, and
	// none is wanted: spawnAndWait runs once per child, so firing there is once per
	// child by construction, and the count does not grow with the turns that child
	// serves. The trap this rules out is the child's system/init line, which looks
	// like the per-child signal and is not — emitModelAnnounced's doc measures it
	// firing once per TURN, so a trigger there would ask on every turn while
	// staying green in any single-turn test.
	//
	// Unlike OnChildExit it fires only when a child ACTUALLY LAUNCHED: the call
	// sits below cmd.Start, so a spawn-setup failure never reaches it. That is the
	// opposite cardinality to OnChildExit's per-supervision-iteration one, and the
	// two fields sit next to each other, so the contrast is stated rather than left
	// to be inferred.
	//
	// The ask is BEST-EFFORT AND ITS ERROR IS ABSORBED — one Debug record, then the
	// spawn continues. It never becomes spawnAndWait's waitErr, so it cannot enter
	// the backoff ladder, restart a child or fail a spawn.
	//
	// It reads Stdin(), NOT the rotation-gated turnTarget, so an ask is not refused
	// while a new_session rotation is armed and one can land on a child
	// RestartFresh is about to kill. Deliberate rather than tolerated: that ask
	// commits nothing and no queue head rides on it, whereas gating it would leave
	// a child whose rotation was ABORTED (BeginRotation's disarm path) permanently
	// unasked — the state the per-replacement ask exists to prevent.
	RequestInitializeOnSpawn bool

	// MCPStatusConfigPath is the daemon-owned MCP config whose exact presence on
	// one child spawn, together with --strict-mcp-config, permits automatic MCP
	// status collection. Empty makes every child ineligible. The path is compared
	// only with that spawn's completed argv; changing Args for a later spawn does
	// not change the policy already installed for the running child.
	MCPStatusConfigPath string

	// SpawnPermissionMode is the session's stored permission posture, written to
	// every spawned child as a set_permission_mode control request before any user
	// turn can reach it (#2064). Optional; the zero value writes nothing and arms
	// nothing, which is what leaves every construction site other than the
	// interactive daemon's mapStreamsupConfig byte-identical.
	//
	// It is the CONSTRUCTION-TIME seed only. The live value is r.spawnMode, which
	// SetSpawnPermissionMode replaces when the session's stored posture changes —
	// Pool.UpdateSettings rebuilds no runner, so a value read from this field at
	// spawn time would go stale and could assert a posture the operator has already
	// changed. Same relationship SessionID has with r.sessionID.
	//
	// bypassPermissions is REFUSED here, and since #2066 that is a SUBTRACTION rather
	// than the same non-membership WritePermissionMode applies: that ticket admitted
	// the escalation to the writer's allow-list so a LIVE child can be escalated in
	// band, and scoped the widening away from this path with
	// permissionModeSpawnWritable. So a bypass session is still sent nothing at spawn
	// and its turns still flow exactly as they do today — a gate no write can ever
	// release is a bricked session, not a fail-closed one — while re-granting bypass
	// now reaches the running child through SetPermissionMode instead of a respawn.
	// The exclusion costs nothing: since #2065 the launch argv already asserts the
	// escalation, so a bypass child is in the right posture before the write would
	// have run, and nothing has measured claude answering an escalation as the FIRST
	// control request on a fresh stream.
	//
	// THAT REFUSAL IS NOT THE ONLY ONE, because this field cannot see every way a child
	// ends up in bypass. The operator's bootstrap pass-through claude args put
	// --dangerously-skip-permissions in the argv without touching SessionSettings, so
	// this field reads default at a child launched in bypass; spawnAndWait consults
	// Config.OperatorBypass for exactly that case and writes nothing. Read that site
	// before treating this field as the whole decision — a live-claude gate, not a
	// review, found the gap.
	//
	// "Sent nothing" is NOT "gate untouched", and the difference was a defect: the gate
	// outlives the child, so a spawn that writes nothing must still arm the OPEN state
	// or it inherits its predecessor's id. See the arm site in spawnAndWait.
	//
	// Setting it REQUIRES PostureGate; New refuses the pairing otherwise. A posture
	// written with nothing correlating its ack is a line the daemon believes in and
	// has not confirmed, which is the shape #2065 cannot tolerate.
	SpawnPermissionMode string

	// OperatorBypass reports that the escalation on this runner's spawn argv came
	// from the OPERATOR's pass-through claude args rather than from the daemon's own
	// settings composition (#2065). It is sessions.RunnerConfig.OperatorBypass
	// carried across the seam, derived there from the settings-free spawnBase.
	// Optional; the zero value is "the daemon composed whatever is on this argv",
	// which is what leaves every construction site outside the interactive daemon
	// byte-identical.
	//
	// TRUE SUPPRESSES THE SPAWN-TIME POSTURE WRITE. It is the second half of that
	// decision, and it REPLACES the argv read that used to make it: until #2065 this
	// runner asked whether the spawn's own args named the escalation, because that
	// answered the same question. It stopped answering it when
	// sessions.claudeSettingsArgs began appending the flag to every argv — a
	// predicate reading the argv now answers "operator bypass" for every session, so
	// no child is ever sent its stored posture and every child stays in the bypass it
	// launched with. Read spawnAndWait's arm site for the whole derivation.
	//
	// It is a Config field where the argv read was per-spawn, and that is sound
	// rather than a regression: the value it replaced was a property of the assembled
	// argv, which Restart(newArgs) can change, whereas provenance is a property of
	// the session's immutable spawnBase, which nothing can. SetSpawnArgs and Restart
	// install an argv VERBATIM, so a future caller composing one from something other
	// than sessions.Session.spawnArgs owns keeping this bit in step — though both
	// mis-pairings fail safe: a flag with the bit false downgrades a child that IS in
	// bypass, and the bit true with no flag writes nothing at a child that is not.
	OperatorBypass bool

	// PostureGate is the ack signal the spawn-time posture write is held against
	// (#2064): armed with the write's locally-minted request_id before the child's
	// stdin is published, released only by that id's success control_response, and
	// consulted by every WriteUserTurn in between. Optional; nil gates nothing.
	//
	// It is minted by the PARSER — (*Parser).PostureGate — because the parser is
	// built before streamsup.New (newStreamRunnerFactory installs it as Config.Stdout)
	// and the release side is the parser's. Minting it there is what makes it
	// impossible to bind the two halves to different gates, which is
	// newSessionParser's argument for minting a parser and its holds together.
	PostureGate *PostureGate

	// onSpawn is an unexported test seam, called once per spawn after cmd.Start
	// and after the stdin handle is stored, with the child's pid. Nil in
	// production. Lets a test observe "child N is up, Stdin() is live" without
	// polling.
	//
	// It fires strictly AFTER the RequestInitializeOnSpawn ask, so when it fires
	// that line is already in the child's pipe. Production cannot tell (the seam is
	// nil there), but it is what makes a test that counts asks deterministic
	// without polling the child's echo.
	onSpawn func(pid int)
}

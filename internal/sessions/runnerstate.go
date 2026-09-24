package sessions

import (
	"log/slog"
	"time"
)

// Phase describes a runner's current lifecycle state.
//
// This and the two types below moved out of internal/supervisor when #1348
// deleted it. They were never supervisor-specific: the stream runner has always
// had to mint a Phase on every State() call and be constructed from a Config,
// so the whole surface travelled through a package named for the one runner
// that no longer exists.
type Phase string

const (
	PhaseStarting Phase = "starting" // before the first child has been spawned
	PhaseRunning  Phase = "running"  // a child process is alive
	PhaseBackoff  Phase = "backoff"  // waiting before the next restart
	PhaseStopped  Phase = "stopped"  // Run has returned
)

// State is a snapshot of a runner's runtime state, returned by Runner.State for
// the control plane.
type State struct {
	Phase        Phase         // current lifecycle phase
	ChildPID     int           // PID of the running child, or 0 when none
	StartedAt    time.Time     // when the runner entered Run
	RestartCount int           // number of times the child has exited
	LastUptime   time.Duration // uptime of the most recent child, zero if first run
	NextBackoff  time.Duration // delay scheduled before the next spawn, zero when running
}

// HarnessClaude is the harness of a session claude runs, and the meaning of an
// empty harness anywhere one is read (#2593). It is the only harness any path
// mints today.
const HarnessClaude = "claude"

// RunnerConfig controls a runner instance. It is what RunnerFactory receives.
//
// It carries only the fields the stream runner actually reads. The supervisor's
// Config carried nine more — resume behaviour, a session-id resolver, a bridge,
// a conversation validator, a transcript resolver, a record directory, two
// fast-crash thresholds and a self-heal hook — every one of which the stream
// factory silently dropped on the floor. Keeping them would have advertised
// behaviour nothing implements.
//
// Two of those dropped fields represent real behaviour the stream path does not
// have, and they are recorded here rather than quietly lost:
//
//   - SelfHeal, the #1165 fast-crash recovery that minted a fresh pinned session
//     id when a child crash-looped on an existing transcript.
//   - ValidateConversation, the unknown-conversation refusal at session build.
//
// Neither has a stream-path equivalent today. If either is wanted back it is a
// feature on the stream runner, not a field to re-add here.
type RunnerConfig struct {
	// ClaudeBin is the path to the claude binary. Defaults to "claude" (found on PATH).
	ClaudeBin string

	// WorkDir is the working directory for the claude child process. Empty means
	// the current directory.
	WorkDir string

	// SessionID is the session this runner drives. The stream runner reads it
	// three times: once for the config it builds, and twice to bind the
	// turn-event sink and the exit channel for that session.
	SessionID string

	// AdoptAnnouncedReset re-keys this runner's session onto an id claude
	// announced a reset to, and draws the client's session delimiter (#2135). The
	// pool sets it to a closure over Pool.AdoptAnnouncedID at both RunnerConfig
	// construction sites; a runner built without one (a test pool, or any factory
	// that does not observe resets) gets nil and the caller skips the call.
	//
	// IT TAKES BOTH IDS, and that is what lets it live on a per-session config at
	// all. SessionID above is construction-fixed and, as its own doc says, does not
	// mirror a rotation — so a closure capturing it would be correct for exactly one
	// reset and fail-closed on every one after that. Carrying no identity of its own,
	// this callback stays correct for the session's whole life and the CALLER supplies
	// the live id (cmd/pyry reads it off the runner's streamSessionTag).
	//
	// It is invoked SYNCHRONOUSLY from the goroutine forwarding claude's stdout, so
	// it must not block for long: it takes Pool.mu and fans out to the transition
	// observer, whose own contract already forbids blocking. It is not a place to do
	// I/O beyond the registry save the re-key already implies.
	AdoptAnnouncedReset func(oldID, newID string) error

	// ClaudeArgs is the extra argv passed through to claude.
	ClaudeArgs []string

	// Harness names the coding agent this session runs (#2593), and the factory
	// selects the runner by it. Both pool construction sites set it canonical, so
	// it is never empty from the pool: HarnessClaude for the bootstrap and every
	// minted session, and a dormant entry's own persisted value for a revive. The
	// sessions package carries the value without validating it; the factory is the
	// one place that decides which harnesses have a runner, and refusing one is an
	// ordinary construction error.
	Harness string

	// PermissionMode is the session's stored permission posture, written to every
	// spawned child in-band and confirmed before any user turn reaches it (#2064).
	// Both construction sites set it from settings.PermissionMode below a
	// canonicalSettings call, so it is always a real mode and never the empty one.
	//
	// It is the CONSTRUCTION-TIME value only, and it goes stale: Pool.UpdateSettings
	// rebuilds no runner, so a posture changed after startup reaches the runner
	// through Runner.SetSpawnPermissionMode instead. This field seeds; that method
	// updates.
	//
	// bypassPermissions travels here like any other mode, and the stream runner still
	// writes nothing for it AT SPAWN — so a bypass session is sent nothing and its
	// turns are never gated. What changed with #2066 is the mechanism, not the
	// outcome: the runner's writer allow-list admits the escalation now, and the spawn
	// path subtracts it back out (permissionModeSpawnWritable) because the launch argv
	// already asserts the escalation on every child since #2065 and nothing has
	// measured claude answering one as a fresh stream's first control request.
	// Re-granting bypass on a RUNNING child no longer waits for a respawn: it goes
	// in-band through Runner.SetPermissionMode.
	PermissionMode string

	// OperatorBypass reports that the escalation reached this spawn's argv from the
	// OPERATOR's pass-through claude args rather than from this package's own
	// settings composition (#2065). Derived at construction by operatorBypass over
	// the session's settings-free base (Session.spawnBase), which is where the
	// pass-through lands verbatim.
	//
	// It is the PROVENANCE signal, and it exists because #2065 made
	// claudeSettingsArgs append --dangerously-skip-permissions to every argv. Both
	// of the daemon's permission fail-safes used to read that flag off the assembled
	// argv, and both invert into their permissive arms the moment the flag is
	// universally present:
	//
	//   - cmd/pyry's withApprovalArgs skipped the approval-flag injection when the
	//     flag was present, so a universally-present flag deletes the daemon's
	//     approval gate for every session.
	//   - internal/streamsup's spawnAndWait suppressed the spawn-time posture write
	//     when the flag was present, so a universally-present flag leaves every child
	//     in bypass and every posture gate open — the exact inverse of #2065.
	//
	// TRUE means the daemon may NOT walk this child back: asserting a stored mode at
	// a child the operator launched in bypass revokes that escalation in-band and
	// unanswerably, which a live-claude gate caught reddening four specs before the
	// interlock existed. FALSE means every escalation on this session's argv is one
	// this package composed, so the stored PermissionMode above decides the posture.
	//
	// It is CONSTRUCTION-FIXED and, unlike PermissionMode, cannot go stale: spawnBase
	// is immutable post-construction, and every post-construction argv install
	// (Pool.UpdateSettings → Session.spawnArgs → Runner.Restart / SetSpawnArgs)
	// recomposes from that same base. A future caller that installs an argv composed
	// from something other than Session.spawnArgs owns keeping this bit in step —
	// though both mis-pairings fail safe: a flag with the bit false downgrades a
	// child that is in bypass, and the bit true with no flag writes nothing at a
	// child that was not.
	//
	// Nothing a remote client controls can set it. SessionConfig.ClaudeArgs is
	// written at one production site, cmd/pyry's daemon composition from the
	// operator's CLI; Pool.Create, Pool.CreateIn, Pool.GetOrCreateIn and Pool.Revive
	// take only a label and a spawn dir.
	OperatorBypass bool

	// Logger is used for the runner's diagnostics. Optional; nil falls back to
	// the package default.
	Logger *slog.Logger

	// Backoff bounds for the respawn ladder.
	BackoffInitial time.Duration
	BackoffMax     time.Duration
	BackoffReset   time.Duration
}

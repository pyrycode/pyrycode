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

	// ClaudeArgs is the extra argv passed through to claude.
	ClaudeArgs []string

	// Logger is used for the runner's diagnostics. Optional; nil falls back to
	// the package default.
	Logger *slog.Logger

	// Backoff bounds for the respawn ladder.
	BackoffInitial time.Duration
	BackoffMax     time.Duration
	BackoffReset   time.Duration
}

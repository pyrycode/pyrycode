package streamsup

import "time"

// Phase describes the runner's current lifecycle state. The values mirror
// supervisor.Phase string-for-string so the cmd/pyry adapter maps one to the
// other by a plain string conversion.
type Phase string

const (
	PhaseStarting Phase = "starting" // before the first child has been spawned
	PhaseRunning  Phase = "running"  // a child process is alive
	PhaseBackoff  Phase = "backoff"  // waiting before the next restart
	PhaseStopped  Phase = "stopped"  // Run has returned
)

// State is a snapshot of the runner's runtime state, the stream-json analogue of
// supervisor.State. It mirrors that type field-for-field: the cmd/pyry adapter
// maps it to supervisor.State (this package must not import internal/supervisor),
// and the control plane's status builder reads all six fields, so all six are
// kept faithful by the Run loop's instrumentation.
type State struct {
	Phase        Phase         // current lifecycle phase
	ChildPID     int           // PID of the running child, or 0 when none
	StartedAt    time.Time     // when the runner entered Run
	RestartCount int           // number of times the child has crashed and backed off
	LastUptime   time.Duration // uptime of the most recent child, zero if first run
	NextBackoff  time.Duration // delay scheduled before the next spawn, zero when running
}

// State returns a snapshot of the current runner state. Safe to call from any
// goroutine. Mirrors supervisor.State().
func (r *Runner) State() State {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.state
}

// updateState applies fn to the runner's state under stateMu. Called only by the
// Run goroutine. Mirrors supervisor.updateState.
func (r *Runner) updateState(fn func(*State)) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	fn(&r.state)
}

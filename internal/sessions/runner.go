package sessions

import "context"

// Runner is the narrow interface internal/sessions dispatches through to drive a
// supervised claude child (Session.sup). It covers exactly the methods the
// session lifecycle invokes on that field — no more — so the Streamrunner
// Interactive work (T4/T7) can inject an alternative runner behind the same
// lifecycle without widening the seam.
//
// *supervisor.Supervisor satisfies Runner structurally with zero supervisor
// edits; the compile-time assertion below is the proof. The concrete supervisor
// still flows through unchanged when Config.RunnerFactory is nil (see
// RunnerFactory) — that nil default keeps today's PTY path byte-identical and is
// the rollback guarantee for this Strangler-Fig slice.
//
// Consumers that need the concrete supervisor (cmd/pyry's turn / cancel wiring)
// reach it via Session.Supervisor(), which deliberately keeps its concrete
// return type. Nothing dispatches those methods through Runner this ticket, so
// they are not on the interface — adding them would be speculative surface.
type Runner interface {
	State() State
	WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error
	WaitForPTY(ctx context.Context) error
	Run(ctx context.Context) error
	Restart(args []string)
}

// RunnerFactory constructs a Runner from a supervisor.Config. It is the
// injection seam on Config: when Config.RunnerFactory is nil, session
// construction defaults to supervisor.New (adapted to the Runner return), so the
// concrete supervisor flows through and the PTY path is byte-identical to today
// — the rollback guarantee. A non-nil factory is invoked at every construction
// site (bootstrap in Pool.New and per-session in Pool.buildSession), letting
// T4/T7 swap in an alternative runner. A returned error propagates through the
// existing "sessions: … supervisor: %w" wraps at both sites.
type RunnerFactory func(cfg RunnerConfig) (Runner, error)

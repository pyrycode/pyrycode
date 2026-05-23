// Package budget enforces the pyry-side --max-turns cap for agent-run by
// counting assistant JSONL entries and signalling the claude child when the
// budget is reached.
//
// In `claude -p` mode, claude exits after --max-turns natively. Interactive
// claude — what `pyry agent-run` spawns — does not, so pyry must enforce the
// cap itself. The Counter is a leaf unit consumable by the downstream
// agent-run driver: wire (*Counter).OnEvent into the loop draining the
// tuidriver.TailJSONL channel, call (*Counter).OnEndOfTurn when
// tuidriver.IsEndTurn fires for that entry, and wire the Counter's
// Terminate / Kill hooks to cmd.Process.Signal(SIGTERM) / SIGKILL
// respectively.
package budget


import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// defaultGracePeriod mirrors supervisor.spawnWaitDelay. Copied (not imported)
// so this package does not couple to the supervisor package.
const defaultGracePeriod = 5 * time.Second

// Reason is the terminal outcome reported by Counter. The zero value ("")
// means neither natural completion nor a budget hit has been observed (e.g.
// the run was torn down by context cancellation).
type Reason string

const (
	// ReasonCompletion means claude reached a natural end_turn signal before
	// the budget was hit.
	ReasonCompletion Reason = "completion"

	// ReasonMaxTurns means the Counter sent SIGTERM at the budget.
	ReasonMaxTurns Reason = "max_turns"
)

// Config configures a Counter. MaxTurns, Terminate, and Kill are required;
// New returns an error otherwise.
type Config struct {
	// MaxTurns is the assistant-entry cap. Required; must be > 0.
	MaxTurns int

	// Terminate is invoked exactly once when the assistant-entry count
	// reaches MaxTurns on a non-end_turn event. Production wires this to
	// cmd.Process.Signal(syscall.SIGTERM).
	Terminate func() error

	// Kill is invoked exactly once after GracePeriod elapses if Stop has not
	// been called. Production wires this to cmd.Process.Signal(syscall.SIGKILL).
	Kill func() error

	// GracePeriod is the SIGTERM→SIGKILL window. Zero defaults to 5s.
	GracePeriod time.Duration

	// Logger is optional; defaults to slog.Default().
	Logger *slog.Logger
}

// Counter counts assistant JSONL entries and enforces the MaxTurns budget by
// invoking Terminate when the count reaches MaxTurns, then escalating to Kill
// after GracePeriod.
//
// Safe for concurrent use. In production OnEvent / OnEndOfTurn fire from the
// agent-run caller's goroutine that drains the tuidriver.TailJSONL channel,
// the grace timer fires from time.AfterFunc's goroutine, and Stop / Reason
// fire from the driver goroutine.
type Counter struct {
	cfg Config

	mu        sync.Mutex
	count     int
	reason    Reason
	fired     bool
	killTimer *time.Timer
}

// New constructs a Counter. Validates required fields, defaults GracePeriod
// to 5s and Logger to slog.Default().
func New(cfg Config) (*Counter, error) {
	if cfg.MaxTurns <= 0 {
		return nil, errors.New("budget: MaxTurns must be > 0")
	}
	if cfg.Terminate == nil {
		return nil, errors.New("budget: nil Terminate")
	}
	if cfg.Kill == nil {
		return nil, errors.New("budget: nil Kill")
	}
	if cfg.GracePeriod == 0 {
		cfg.GracePeriod = defaultGracePeriod
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Counter{cfg: cfg}, nil
}

// OnEvent counts assistant entries and triggers Terminate when the budget is
// reached. Caller invokes OnEvent once per entry surfaced by
// tuidriver.TailJSONL. End-of-turn classification is the caller's job — it
// calls OnEndOfTurn separately when tuidriver.IsEndTurn(entry) is true. The
// budget check fires synchronously here for every assistant entry, including
// one that is itself end-of-turn: at the boundary (MaxTurns reached exactly
// at the end-of-turn entry), max_turns wins because OnEvent grabs the
// reason first and the subsequent OnEndOfTurn cannot overwrite a non-empty
// reason.
func (c *Counter) OnEvent(entry tuidriver.JSONLEntry) {
	if entry.Type != "assistant" {
		return
	}
	c.mu.Lock()
	c.count++
	if c.fired {
		c.mu.Unlock()
		return
	}
	if c.count < c.cfg.MaxTurns {
		c.mu.Unlock()
		return
	}
	c.fired = true
	c.reason = ReasonMaxTurns
	count, max := c.count, c.cfg.MaxTurns
	c.killTimer = time.AfterFunc(c.cfg.GracePeriod, c.killAfterGrace)
	c.mu.Unlock()

	c.cfg.Logger.Info("budget: max turns reached, sending SIGTERM",
		slog.Int("count", count),
		slog.Int("max_turns", max))
	if err := c.cfg.Terminate(); err != nil {
		c.cfg.Logger.Warn("budget: terminate failed",
			slog.Int("count", count),
			slog.Int("max_turns", max),
			"err", err)
	}
}

// OnEndOfTurn records completion as the terminal reason unless the budget
// has already fired. Caller invokes once per entry for which
// tuidriver.IsEndTurn returns true, after OnEvent has been called for the
// same entry.
func (c *Counter) OnEndOfTurn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reason == "" {
		c.reason = ReasonCompletion
	}
}

// Reason returns the terminal outcome observed so far. Stable after the
// driver's watcher returns; before that it reports the latest observation.
func (c *Counter) Reason() Reason {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

// Stop cancels any pending SIGKILL grace timer. Idempotent. Safe to call
// regardless of whether the budget was hit.
func (c *Counter) Stop() {
	c.mu.Lock()
	t := c.killTimer
	c.killTimer = nil
	c.mu.Unlock()
	if t != nil {
		t.Stop()
	}
}

// killAfterGrace fires from time.AfterFunc when the SIGTERM→SIGKILL window
// elapses without Stop having cancelled it.
func (c *Counter) killAfterGrace() {
	c.mu.Lock()
	if c.killTimer == nil {
		c.mu.Unlock()
		return
	}
	c.killTimer = nil
	reason := c.reason
	c.mu.Unlock()

	c.cfg.Logger.Warn("budget: grace period elapsed, sending SIGKILL",
		slog.String("reason", string(reason)))
	if err := c.cfg.Kill(); err != nil {
		c.cfg.Logger.Warn("budget: kill failed", "err", err)
	}
}

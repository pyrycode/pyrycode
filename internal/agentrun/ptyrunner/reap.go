package ptyrunner

import (
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
)

// killGrace is the SIGTERM → SIGKILL grace applied via exec.Cmd.WaitDelay when
// the operator cancels Run's context. It mirrors streamrunner.killGrace and
// supervisor.spawnWaitDelay (both 5s). The exact value is non-binding: the
// effective bounded-exit backstop is tui-driver Session.Close's shorter
// shutdown grace, which always fires first. WaitDelay only has to stay ≥ that
// grace so os/exec does not preempt Close's graceful path with an early
// SIGKILL of claude.
const killGrace = 5 * time.Second

// reapDescendantGroupsFn is the seam every teardown reap call site routes
// through so tests can observe the reap firing on the budget-hit and
// watchdog-fire paths without standing up a real descendant process tree.
// Production leaves it pointed at agentrun.ReapDescendantGroups (the reaper was
// lifted into the shared agentrun package in #923 so streamrunner can consume
// it too; byte-identical to a direct call); the budget/watchdog unit tests swap
// it non-parallel and restore the original via t.Cleanup.
var reapDescendantGroupsFn = agentrun.ReapDescendantGroups

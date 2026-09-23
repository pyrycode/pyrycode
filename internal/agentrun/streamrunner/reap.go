package streamrunner

import (
	"github.com/pyrycode/pyrycode/internal/agentrun"
)

// reapDescendantGroupsFn is the seam the teardown reap call site routes through
// so the unit test can observe the reap firing on the operator-SIGTERM teardown
// without standing up a real descendant process tree. Production leaves it
// pointed at agentrun.ReapDescendantGroups (the reaper lives in the shared
// agentrun package, #923, so streamsup consumes it too; byte-identical to a
// direct call); the ctx-cancel unit test swaps it non-parallel and restores the
// original via t.Cleanup.
var reapDescendantGroupsFn = agentrun.ReapDescendantGroups

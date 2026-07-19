package streamsup

import (
	"github.com/pyrycode/pyrycode/internal/agentrun"
)

// reapDescendantGroupsFn is the seam the teardown reap call site routes through
// so the unit test can observe the reap firing on the operator-SIGTERM teardown
// without standing up a real descendant process tree. Production leaves it
// pointed at agentrun.ReapDescendantGroups (byte-identical to a direct call);
// the ctx-cancel unit test swaps it non-parallel and restores the original via
// t.Cleanup. Mirrors streamrunner/reap.go's seam — the shared reaper lives in
// internal/agentrun (the parent package this slice is allowed to import), not
// in a sibling agentrun subpackage.
var reapDescendantGroupsFn = agentrun.ReapDescendantGroups

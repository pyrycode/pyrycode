package streamrunner

import (
	"github.com/pyrycode/pyrycode/internal/agentrun"
)

// reapDescendantGroupsFn is the seam the teardown reap call site routes through
// so the unit test can observe the reap firing on the operator-SIGTERM teardown
// without standing up a real descendant process tree. Production leaves it
// pointed at agentrun.ReapDescendantGroups (the reaper was lifted into the
// shared agentrun package in #923 so streamrunner can consume it alongside
// ptyrunner; byte-identical to a direct call); the ctx-cancel unit test swaps it
// non-parallel and restores the original via t.Cleanup. Mirrors
// ptyrunner/reap.go's seam (without ptyrunner's killGrace const — streamrunner
// already defines its own at runner.go).
var reapDescendantGroupsFn = agentrun.ReapDescendantGroups

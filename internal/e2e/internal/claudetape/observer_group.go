package claudetape

import (
	"os"
	"syscall"
)

// The original fallback test's Python observer owns the production PGID and
// directly parents our finite helper. All other profiles retain direct ownership.
func fallbackWorkerGroupShape(job FallbackWorkerJob) bool {
	if job.OwnerPID <= 1 || job.GroupPID <= 1 {
		return false
	}
	if job.Scenario == "fallback-suggestion" {
		return job.OwnerPID != job.GroupPID
	}
	return job.GroupPID == job.OwnerPID
}
func validateFallbackWorkerKernelGroup(job FallbackWorkerJob, peer int) error {
	group, err := syscall.Getpgid(peer)
	if err != nil || peer != job.OwnerPID || !fallbackWorkerGroupShape(job) || group != job.GroupPID {
		return os.ErrInvalid
	}
	if job.Scenario != "fallback-suggestion" {
		return nil
	}
	parent, err := currentProcessParent(peer)
	if err != nil || parent != job.GroupPID {
		return os.ErrInvalid
	}
	daemon, err := currentProcessParent(job.GroupPID)
	if err != nil || daemon != job.DaemonPID {
		return os.ErrInvalid
	}
	ownerGroup, err := syscall.Getpgid(job.GroupPID)
	if err != nil || ownerGroup != job.GroupPID {
		return os.ErrInvalid
	}
	return nil
}
func fallbackWorkerObservationPID(job FallbackWorkerJob) int {
	if job.Scenario == "fallback-suggestion" {
		return job.GroupPID
	}
	return job.OwnerPID
}

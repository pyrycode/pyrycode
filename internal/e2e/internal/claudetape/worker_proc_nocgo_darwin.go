//go:build darwin && !cgo

package claudetape

// Without libproc, native observations remain unknown and cannot authorize an
// unregistered cancellation. No direct syscall substitutes for that boundary.
func workerNativeProcInfo(int, int, uintptr, []byte) int { return 0 }
func workerNativeChildPIDs(int, []int32) int             { return 0 }
func workerNativePath(int, []byte) int                   { return 0 }

//go:build darwin && cgo

package claudetape

/*
#include <libproc.h>
*/
import "C"

import "unsafe"

// libproc wrappers preserve Darwin's supported native query boundary. Buffers
// contain no Go pointers and are used synchronously, never retained by C.
func workerNativeProcInfo(pid, flavor int, address uintptr, raw []byte) int {
	return int(C.proc_pidinfo(C.int(pid), C.int(flavor), C.uint64_t(address), unsafe.Pointer(&raw[0]), C.int(len(raw))))
}

func workerNativeChildPIDs(pid int, children []int32) int {
	return int(C.proc_listchildpids(C.pid_t(pid), unsafe.Pointer(&children[0]), C.int(len(children)*4)))
}

func workerNativePath(pid int, path []byte) int {
	return int(C.proc_pidpath(C.int(pid), unsafe.Pointer(&path[0]), C.uint32_t(len(path))))
}

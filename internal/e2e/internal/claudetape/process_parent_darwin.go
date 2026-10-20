//go:build darwin

package claudetape

import (
	"golang.org/x/sys/unix"
	"os"
)

// Only a current socket peer or its current group owner is queried.
func currentProcessParent(pid int) (int, error) {
	if pid <= 1 {
		return 0, os.ErrInvalid
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	if info == nil || int(info.Proc.P_pid) != pid || info.Eproc.Ppid <= 1 {
		return 0, os.ErrInvalid
	}
	return int(info.Eproc.Ppid), nil
}

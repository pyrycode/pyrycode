//go:build darwin

package e2emcp

import (
	"bytes"
	"encoding/binary"
	"golang.org/x/sys/unix"
	"net"
	"os"
)

func currentPeer(c *net.UnixConn) (pid, parent int, executable string, err error) {
	raw, e := c.SyscallConn()
	if e != nil {
		return 0, 0, "", protocolError()
	}
	e = raw.Control(func(fd uintptr) {
		cred, x := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if x != nil || cred.Uid != uint32(os.Geteuid()) {
			err = protocolError()
			return
		}
		pid, err = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	if e != nil || err != nil || pid <= 1 {
		return 0, 0, "", protocolError()
	}
	info, e := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if e != nil || info == nil || int(info.Proc.P_pid) != pid || info.Eproc.Ppid <= 1 {
		return 0, 0, "", protocolError()
	}
	// KERN_PROCARGS2 begins with argc and the kernel executable path. Never
	// retain or emit the following argv/environment buffer.
	args, e := unix.SysctlRaw("kern.procargs2", pid)
	if e != nil || len(args) < 6 || binary.NativeEndian.Uint32(args[:4]) == 0 {
		return 0, 0, "", protocolError()
	}
	end := bytes.IndexByte(args[4:], 0)
	if end <= 0 {
		return 0, 0, "", protocolError()
	}
	return pid, int(info.Eproc.Ppid), string(args[4 : 4+end]), nil
}

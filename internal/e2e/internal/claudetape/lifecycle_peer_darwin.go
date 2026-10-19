//go:build darwin

package claudetape

import (
	"errors"
	"golang.org/x/sys/unix"
	"net"
	"os"
)

func lifecyclePeerPID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	pid := 0
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil || cred.Uid != uint32(os.Geteuid()) {
			socketErr = errors.New("foreign lifecycle peer")
			return
		}
		pid, socketErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	if err != nil {
		return 0, err
	}
	return pid, socketErr
}

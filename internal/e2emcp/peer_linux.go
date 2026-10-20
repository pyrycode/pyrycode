//go:build linux

package e2emcp

import (
	"fmt"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"strconv"
	"strings"
)

func currentPeer(c *net.UnixConn) (pid, parent int, executable string, err error) {
	raw, e := c.SyscallConn()
	if e != nil {
		return 0, 0, "", protocolError()
	}
	e = raw.Control(func(fd uintptr) {
		cred, x := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if x != nil || cred.Uid != uint32(os.Geteuid()) {
			err = protocolError()
			return
		}
		pid = int(cred.Pid)
	})
	if e != nil || err != nil || pid <= 1 {
		return 0, 0, "", protocolError()
	}
	data, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return 0, 0, "", protocolError()
	}
	text := string(data)
	end := strings.LastIndex(text, ") ")
	if end < 0 || !strings.HasPrefix(text, strconv.Itoa(pid)+" (") {
		return 0, 0, "", protocolError()
	}
	fields := strings.Fields(text[end+2:])
	if len(fields) < 2 {
		return 0, 0, "", protocolError()
	}
	parent, e = strconv.Atoi(fields[1])
	if e != nil || parent <= 1 {
		return 0, 0, "", protocolError()
	}
	executable, e = os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if e != nil {
		return 0, 0, "", protocolError()
	}
	return pid, parent, executable, nil
}

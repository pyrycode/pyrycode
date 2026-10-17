//go:build darwin

package canonicalpath

import (
	"bytes"
	"path/filepath"
	"syscall"
	"unsafe"
)

// nativePath asks the kernel for current on-disk spelling without enumerating
// ancestors. O_EVTONLY opens metadata rather than file contents, including for
// file leaves. O_NONBLOCK also bounds opening special leaves without a peer.
// Every call observes the filesystem afresh.
func nativePath(path string) (string, bool) {
	return descriptorPath(path, syscall.Open, getDescriptorPath, syscall.Close)
}

func descriptorPath(path string, open func(string, int, uint32) (int, error), query func(int) (string, error), close func(int) error) (string, bool) {
	fd, err := open(path, syscall.O_EVTONLY|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", false
	}
	canonical, queryErr := query(fd)
	closeErr := close(fd)
	if queryErr != nil || closeErr != nil || !filepath.IsAbs(canonical) {
		return "", false
	}
	return canonical, true
}

func getDescriptorPath(fd int) (string, error) {
	// Darwin's F_GETPATH requires a MAXPATHLEN-sized buffer.
	var buf [1024]byte
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETPATH, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return "", errno
	}
	n := bytes.IndexByte(buf[:], 0)
	if n < 0 {
		return "", syscall.ENAMETOOLONG
	}
	return string(buf[:n]), nil
}

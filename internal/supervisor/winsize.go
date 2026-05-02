package supervisor

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// watchWindowSize forwards SIGWINCH from the controlling terminal to the PTY
// so that the child process sees the correct terminal dimensions. Returns a
// function to stop watching.
func (s *Supervisor) watchWindowSize(ptmx *os.File) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})

	// Prime it once so the child starts with the correct size.
	resizeOnce(ptmx)

	go func() {
		for {
			select {
			case <-ch:
				resizeOnce(ptmx)
			case <-done:
				return
			}
		}
	}()

	return func() {
		signal.Stop(ch)
		close(done)
	}
}

func resizeOnce(ptmx *os.File) {
	// term.IsTerminal uses ioctl directly without wrapping the fd in an
	// *os.File. Earlier code used `pty.GetsizeFull(os.NewFile(fd, ""))`,
	// which leaks the wrapper to GC; the wrapper's finalizer eventually
	// calls syscall.close on the underlying fd. Under heavy parallel test
	// load the kernel reuses fd numbers fast — a stale finalizer from one
	// resizeOnce call can close a different test's fd, surfacing as
	// intermittent EBADF on f.Close (e.g. saveRegistryLocked's temp file).
	// See PROJECT-MEMORY 2026-05-02 for the diagnostic chain.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return
	}
	size, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		return
	}
	_ = pty.Setsize(ptmx, size)
}

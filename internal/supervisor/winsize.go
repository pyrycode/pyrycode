package supervisor

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
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
	if !isTerminal(os.Stdin.Fd()) {
		return
	}
	size, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		return
	}
	_ = pty.Setsize(ptmx, size)
}

func isTerminal(fd uintptr) bool {
	_, err := pty.GetsizeFull(os.NewFile(fd, ""))
	return err == nil
}

package agentrun

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ExitErrIsBenign reports whether err is the OS-level "process already
// gone" or "self-signalled exit" shape that surfaces during expected
// teardown of an agent-run child. Returns false for nil and for any
// other error. Wrapped errors are unwrapped via errors.Is / errors.As.
//
// Benign classes:
//   - syscall.ESRCH      (no such process — kill/signal raced child exit)
//   - syscall.EPIPE      (broken pipe — fd write after child exit)
//   - os.ErrClosed       (write/close after fd already closed)
//   - *exec.ExitError where ExitCode() == 143 (SIGTERM) or 137 (SIGKILL)
//   - *exec.ExitError where the child was signal-killed by SIGTERM or
//     SIGKILL (Signaled() with the matching signal — covers the case
//     where the child has no signal handler and ExitCode() returns -1).
func ExitErrIsBenign(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ESRCH) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, os.ErrClosed) {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code == 143 || code == 137 {
			return true
		}
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			switch ws.Signal() {
			case syscall.SIGTERM, syscall.SIGKILL:
				return true
			}
		}
	}
	return false
}

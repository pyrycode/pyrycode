package control

import (
	"errors"
	"syscall"
)

// isTransientStartupError reports whether err matches the dial-error shape
// produced when the daemon is mid-restart: either the unix socket file
// does not exist yet (syscall.ENOENT) or the file exists but the daemon
// has not yet begun accepting connections (syscall.ECONNREFUSED).
//
// Returns false for nil, for any other syscall, and for higher-level
// failures (timeouts, EOF, protocol errors). #199 wires this into a
// bounded retry loop in the client dial path.
func isTransientStartupError(err error) bool {
	return errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ECONNREFUSED)
}

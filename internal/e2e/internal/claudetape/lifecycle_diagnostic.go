package claudetape

import (
	"errors"
	"os"
	"syscall"
)

// LifecycleDiagnostic is content-free terminal state, never completion authority.
type LifecycleDiagnostic struct {
	Stage, ErrorClass                                             string
	Frames, PlayerNext, PlayerFrames, PendingInput, PendingOutput int
	InputEOF, OutputEOF, StderrEOF, Sealed, ActionAccepted        bool
}

func lifecycleErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, syscall.EPIPE):
		return "broken-pipe"
	case errors.Is(err, os.ErrDeadlineExceeded):
		return "deadline"
	default:
		return "rejected"
	}
}

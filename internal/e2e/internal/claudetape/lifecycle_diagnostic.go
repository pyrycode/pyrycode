package claudetape

import (
	"encoding/json"
	"errors"
	"os"
	"syscall"
)

// LifecycleDiagnostic is content-free terminal state, never completion authority.
type LifecycleDiagnostic struct {
	InputKind                                                     string
	Stage, ErrorClass                                             string
	Frames, PlayerNext, PlayerFrames, PendingInput, PendingOutput int
	InputEOF, OutputEOF, StderrEOF, Sealed, ActionAccepted        bool
	ActionAcknowledged, TerminalChecked, Completed                bool
	InputSampled, OutputSampled, StderrSampled                    bool
	InputsMatched, OutputsDelivered, ProviderInputsForwarded      int
	// Maxima retain the output ordinal for each timing substage independently.
	// Order: restoration, BeforeOutput observation, replay projection, forwarding.
	OutputWorkMaxNS      [4]int64
	OutputWorkMaxOrdinal [4]int
	// Ring of at most 64 samples: ordinal, anchor, due, entry, four work stages,
	// completion (relative to that anchor), and process CPU elapsed. CPU includes
	// other broker goroutines; it cannot attribute a historical scheduling stall.
	OutputTiming [][10]int64
}

func lifecycleCPUTime() int64 {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return -1
	}
	return usage.Utime.Nano() + usage.Stime.Nano()
}

func lifecycleInputKind(raw []byte) string {
	var frame struct {
		Type    string
		Request struct{ Subtype string }
	}
	if json.Unmarshal(raw, &frame) != nil {
		return "invalid"
	}
	switch frame.Type {
	case "user", "control_response":
		return frame.Type
	case "control_request":
		switch frame.Request.Subtype {
		case "initialize", "interrupt", "get_context_usage", "mcp_status", "get_settings":
			return "control_request/" + frame.Request.Subtype
		}
		return "control_request/other"
	default:
		return "other"
	}
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
